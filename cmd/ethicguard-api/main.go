package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ethicguard/ethicguard-api/internal/config"
	"github.com/ethicguard/ethicguard-api/internal/httpapi"
	"github.com/ethicguard/ethicguard-api/internal/jobs"
	"github.com/ethicguard/ethicguard-api/internal/llm"
	"github.com/ethicguard/ethicguard-api/internal/store"
	"github.com/ethicguard/ethicguard-api/internal/version"
	"github.com/ethicguard/ethicguard-api/internal/worker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}

	logger.Info("starting ethicguard-api",
		"version", version.Version,
		"commit", version.Commit,
		"env", cfg.Env,
		"addr", cfg.HTTPAddr,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Connect to Postgres (if configured) and run migrations. In dev with no
	// DATABASE_URL set, skip the DB so the API still boots for smoke tests.
	var st *store.Store
	var installations *store.Installations
	var projectsRepo *store.Projects
	var auditsRepo *store.Audits
	var jobsRepo *store.Jobs
	var findingsRepo *store.Findings
	if cfg.DatabaseURL != "" {
		s, err := store.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			logger.Error("postgres open failed", "err", err)
			os.Exit(1)
		}
		defer s.Close()
		if err := s.Migrate(ctx, logger); err != nil {
			logger.Error("migrations failed", "err", err)
			os.Exit(1)
		}
		st = s
		installations = &store.Installations{Store: st}
		projectsRepo = &store.Projects{Store: st}
		auditsRepo = &store.Audits{Store: st}
		jobsRepo = &store.Jobs{Store: st}
		findingsRepo = &store.Findings{Store: st}
		logger.Info("postgres connected and migrated")
	} else {
		logger.Warn("ETHICGUARD_DATABASE_URL empty, running without postgres (dev only)")
	}

	// LLM client — nil in dev without an API key, which disables analysis
	// workers but lets the server boot for smoke tests.
	var llmClient *llm.Client
	if cfg.AnthropicAPIKey != "" {
		llmClient = llm.New(cfg.AnthropicAPIKey, cfg.AnthropicModel)
		logger.Info("llm client initialized", "model", cfg.AnthropicModel)
	} else {
		logger.Warn("ETHICGUARD_ANTHROPIC_API_KEY empty, analysis worker disabled")
	}

	// In-memory payload bus between handler and worker. Lives as long as the
	// process; payload entries are deleted as workers pick them up.
	queue := jobs.New()

	// Start the worker pool only when both DB and LLM are configured. Without
	// either, /v1/analysis correctly returns 503-style errors because the
	// handler is gated on Jobs being set below.
	if jobsRepo != nil && findingsRepo != nil && llmClient != nil {
		pool := worker.New(worker.Deps{
			Logger:   logger,
			Jobs:     jobsRepo,
			Findings: findingsRepo,
			Queue:    queue,
			LLM:      llmClient,
		}, cfg.WorkerConcurrency, 5*time.Second)
		pool.Start(ctx)
		logger.Info("worker pool started", "concurrency", cfg.WorkerConcurrency)
	}

	// Pass repositories through nil-safe assignment: putting a typed nil
	// pointer into an interface field would leave Deps.Projects != nil, which
	// would let the router register handlers that crash on first call.
	deps := httpapi.Deps{
		Logger:          logger,
		Installations:   installations,
		InstallerSecret: cfg.InstallerSecret,
		JWTAudience:     cfg.JWTAudience,
		Queue:           queue,
	}
	if projectsRepo != nil {
		deps.Projects = projectsRepo
	}
	if auditsRepo != nil {
		deps.Audits = auditsRepo
	}
	if jobsRepo != nil {
		deps.Jobs = jobsRepo
	}
	if findingsRepo != nil {
		deps.Findings = findingsRepo
	}
	router := httpapi.NewRouter(deps)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown failed", "err", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}
