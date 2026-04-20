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

	"github.com/ethicguard/ethicguard-api/internal/analysis"
	"github.com/ethicguard/ethicguard-api/internal/catalog"
	"github.com/ethicguard/ethicguard-api/internal/config"
	digestpkg "github.com/ethicguard/ethicguard-api/internal/digests"
	"github.com/ethicguard/ethicguard-api/internal/httpapi"
	"github.com/ethicguard/ethicguard-api/internal/jobs"
	"github.com/ethicguard/ethicguard-api/internal/learning"
	"github.com/ethicguard/ethicguard-api/internal/llm"
	"github.com/ethicguard/ethicguard-api/internal/store"
	"github.com/ethicguard/ethicguard-api/internal/version"
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
		"worker_concurrency", cfg.WorkerConcurrency,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cat, err := catalog.Load()
	if err != nil {
		logger.Error("catalog load failed", "err", err)
		os.Exit(1)
	}
	logger.Info("catalog loaded", "keys", len(cat.Keys()))

	// Postgres is optional in dev so the server can boot for smoke tests.
	var (
		st              *store.Store
		installations   *store.Installations
		projects        *store.Projects
		jobsRepo        *store.Jobs
		findings        *store.Findings
		findingActions  *store.FindingActions
		userPreferences *store.UserPreferences
		digestsRepo     *store.Digests
		audit           *store.Audit
	)
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
		projects = &store.Projects{Store: st}
		jobsRepo = &store.Jobs{Store: st}
		findings = &store.Findings{Store: st}
		findingActions = &store.FindingActions{Store: st}
		userPreferences = &store.UserPreferences{Store: st}
		digestsRepo = &store.Digests{Store: st}
		audit = &store.Audit{Store: st}
		logger.Info("postgres connected and migrated")
	} else {
		logger.Warn("ETHICGUARD_DATABASE_URL empty, running without postgres (dev only)")
	}

	// LLM clients — default + heavy. Heavy is used on escalation ("Go deeper")
	// and is opt-in per request; without an API key both are nil and the
	// /v1/analysis path is disabled.
	var llmClient *llm.Client
	var llmHeavy *llm.Client
	if cfg.AnthropicAPIKey != "" {
		llmClient = llm.New(cfg.AnthropicAPIKey, cfg.AnthropicModel)
		logger.Info("llm client initialized", "model", cfg.AnthropicModel)
		if cfg.AnthropicModelHeavy != "" && cfg.AnthropicModelHeavy != cfg.AnthropicModel {
			llmHeavy = llm.New(cfg.AnthropicAPIKey, cfg.AnthropicModelHeavy)
			logger.Info("llm heavy client initialized", "model", cfg.AnthropicModelHeavy)
		}
	} else {
		logger.Warn("ETHICGUARD_ANTHROPIC_API_KEY empty, /v1/analysis disabled")
	}

	// Worker pool — only useful when we have both DB and LLM.
	var worker *jobs.Worker
	if jobsRepo != nil && findings != nil && audit != nil && llmClient != nil {
		var heavyLLM analysis.LLM
		if llmHeavy != nil {
			heavyLLM = llmHeavy
		}
		worker = jobs.NewWithHeavy(logger, jobs.Config{
			PoolSize:  cfg.WorkerConcurrency,
			Buffer:    cfg.WorkerConcurrency * 16,
			SweepAge:  2 * time.Minute,
			SweepTick: 30 * time.Second,
		}, jobsRepo, findings, audit, llmClient, heavyLLM, cat)
		go worker.Run(ctx)
		logger.Info("worker pool started",
			"concurrency", cfg.WorkerConcurrency,
			"heavy_enabled", heavyLLM != nil,
		)
	}

	deps := httpapi.Deps{
		Logger:          logger,
		Installations:   installations,
		Projects:        projects,
		Jobs:            jobsRepo,
		Findings:        findings,
		FindingActions:  findingActions,
		UserPreferences: userPreferences,
		Digests:         digestsRepo,
		Audit:           audit,
		Catalog:         cat,
		InstallerSecret: cfg.InstallerSecret,
		JWTAudience:     cfg.JWTAudience,
	}
	if worker != nil {
		deps.Dispatcher = worker
	}
	router := httpapi.NewRouter(deps)

	// Background loops: the learning loop (Phase 2 #7) re-tunes per-category
	// thresholds from recent dismissals, the digest generator (Phase 3 #11)
	// snapshots notable findings weekly. Both are cheap to run — SELECTs
	// against small aggregate queries — so a simple ticker pattern is fine.
	if projects != nil && audit != nil {
		go runLearningLoop(ctx, logger, projects, audit, st)
	}
	if digestsRepo != nil && audit != nil {
		gen := &digestpkg.Generator{Logger: logger, Digests: digestsRepo, Audit: audit}
		go runDigestLoop(ctx, logger, gen)
	}

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

// runLearningLoop fires the dismissal-driven threshold tuner once 5 minutes
// after boot (so the server is warm and any install lifecycle has settled)
// and every 24 hours thereafter. Runs inline on its own goroutine.
func runLearningLoop(ctx context.Context, logger *slog.Logger, projects *store.Projects, audit *store.Audit, st *store.Store) {
	initial := time.NewTimer(5 * time.Minute)
	defer initial.Stop()
	select {
	case <-ctx.Done():
		return
	case <-initial.C:
	}
	if err := learning.RunOnce(ctx, logger, projects, audit, st); err != nil {
		logger.Error("learning loop first run failed", "err", err)
	}
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := learning.RunOnce(ctx, logger, projects, audit, st); err != nil {
				logger.Error("learning loop run failed", "err", err)
			}
		}
	}
}

// runDigestLoop wakes once a day and only actually runs on Mondays around
// 02:00 UTC. Keeps the scheduler state stateless — if the binary restarts
// on a Monday we may run twice, which is fine (digests are additive).
func runDigestLoop(ctx context.Context, logger *slog.Logger, gen *digestpkg.Generator) {
	t := time.NewTicker(1 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if now.UTC().Weekday() == time.Monday && now.UTC().Hour() == 2 {
				if err := gen.GenerateForAll(ctx, now); err != nil {
					logger.Error("digest loop run failed", "err", err)
				}
			}
		}
	}
}
