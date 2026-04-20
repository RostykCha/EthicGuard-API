// Package jobs owns the async analysis pipeline: a buffered channel fed by
// the POST handler, consumed by a pool of goroutines that call Claude and
// write findings.
//
// Zero-retention design note: the issue payload never hits Postgres. It
// lives only in memory — handed from the POST handler to the worker over
// the in-memory channel. If the server restarts while a job is queued but
// not yet claimed, the payload is gone; the jobs.sweepQueued janitor marks
// those rows as failed with code "payload_lost" on boot.
package jobs

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ethicguard/ethicguard-api/internal/analysis"
	"github.com/ethicguard/ethicguard-api/internal/catalog"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

// Work is the in-memory envelope the POST handler hands to the worker.
//
// Model selects which LLM client the worker uses: "" or "default" picks
// claude-sonnet-4-6; "heavy" picks claude-opus-4-6 (Phase 2 #8 escalation).
// Unknown values fall back to the default client.
type Work struct {
	JobID          int64
	InstallationID int64
	Model          string
	Request        *analysis.AnalysisRequest
}

// ErrBusy is returned by Dispatcher.Dispatch when the channel is full. The
// POST handler responds 503 in that case — better than silently losing the
// payload by leaving the row stuck in queued state.
var ErrBusy = errors.New("jobs: dispatcher busy")

// Dispatcher is the bridge between the POST handler and the worker pool.
// Implemented by *Worker; abstracted so tests can swap in a fake.
type Dispatcher interface {
	Dispatch(ctx context.Context, w Work) error
}

// JobsRepo is the slice of *store.Jobs the worker depends on. Declared as an
// interface so tests can substitute an in-memory fake.
type JobsRepo interface {
	MarkRunning(ctx context.Context, jobID int64) error
	MarkDone(ctx context.Context, jobID int64) error
	MarkFailed(ctx context.Context, jobID int64, code string) error
	SweepQueuedOlderThan(ctx context.Context, age time.Duration) (int64, error)
}

// FindingsRepo is the slice of *store.Findings the worker depends on.
type FindingsRepo interface {
	Insert(ctx context.Context, jobID int64, in store.FindingInput) error
}

// AuditRepo is the slice of *store.Audit the worker depends on.
type AuditRepo interface {
	Log(ctx context.Context, installationID int64, actorAccountID, action, target string, meta map[string]any) error
}

// Worker is the pool that claims jobs from the channel and runs analysis.
//
// llm is the default (sonnet) client; llmHeavy is the opus client used when
// a Work item carries Model=="heavy". llmHeavy may be nil, in which case
// escalation silently falls back to the default client — the request still
// succeeds, just at default fidelity.
type Worker struct {
	logger   *slog.Logger
	jobs     JobsRepo
	findings FindingsRepo
	audit    AuditRepo
	llm      analysis.LLM
	llmHeavy analysis.LLM
	cat      *catalog.Catalog

	ch        chan Work
	poolSize  int
	sweepAge  time.Duration
	sweepTick time.Duration
}

// Config configures a new Worker. Buffer sets the channel capacity; when full,
// Dispatch returns ErrBusy. PoolSize is the number of goroutines.
type Config struct {
	PoolSize  int
	Buffer    int
	SweepAge  time.Duration // how old a queued row must be before janitor fails it
	SweepTick time.Duration // how often the janitor runs
}

// New constructs a Worker with a single LLM client (the default). Use
// NewWithHeavy to wire an opus client for escalation.
func New(logger *slog.Logger, cfg Config, jobsRepo JobsRepo, findingsRepo FindingsRepo, audit AuditRepo, llm analysis.LLM, cat *catalog.Catalog) *Worker {
	return NewWithHeavy(logger, cfg, jobsRepo, findingsRepo, audit, llm, nil, cat)
}

// NewWithHeavy constructs a Worker that can route jobs to a secondary heavy
// LLM client (Opus) when Work.Model == "heavy".
func NewWithHeavy(logger *slog.Logger, cfg Config, jobsRepo JobsRepo, findingsRepo FindingsRepo, audit AuditRepo, llm analysis.LLM, llmHeavy analysis.LLM, cat *catalog.Catalog) *Worker {
	if cfg.PoolSize < 1 {
		cfg.PoolSize = 1
	}
	if cfg.Buffer < cfg.PoolSize {
		cfg.Buffer = cfg.PoolSize * 16
	}
	if cfg.SweepAge <= 0 {
		cfg.SweepAge = 2 * time.Minute
	}
	if cfg.SweepTick <= 0 {
		cfg.SweepTick = 30 * time.Second
	}
	return &Worker{
		logger:    logger.With("component", "jobs.worker"),
		jobs:      jobsRepo,
		findings:  findingsRepo,
		audit:     audit,
		llm:       llm,
		llmHeavy:  llmHeavy,
		cat:       cat,
		ch:        make(chan Work, cfg.Buffer),
		poolSize:  cfg.PoolSize,
		sweepAge:  cfg.SweepAge,
		sweepTick: cfg.SweepTick,
	}
}

// Dispatch sends work into the buffered channel without blocking. Returns
// ErrBusy if the buffer is full; the caller (POST handler) must treat this
// as a 503 and clean up the job row.
func (w *Worker) Dispatch(_ context.Context, work Work) error {
	select {
	case w.ch <- work:
		return nil
	default:
		return ErrBusy
	}
}

// Run starts PoolSize consumer goroutines + a sweep janitor. Blocks until
// ctx is cancelled, then drains in-flight work and returns.
func (w *Worker) Run(ctx context.Context) {
	// Initial sweep: any queued rows lingering from a previous boot have lost
	// their in-memory payload. Fail them with a stable code.
	if n, err := w.jobs.SweepQueuedOlderThan(ctx, 0); err != nil {
		w.logger.Error("initial sweep failed", "err", err)
	} else if n > 0 {
		w.logger.Info("initial sweep failed orphaned jobs", "count", n)
	}

	var wg sync.WaitGroup
	for i := 0; i < w.poolSize; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			w.consume(ctx, id)
		}(i)
	}

	// Janitor: periodically fail any queued rows older than SweepAge.
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.sweepLoop(ctx)
	}()

	<-ctx.Done()
	// Close channel so consumers finish draining, then wait.
	close(w.ch)
	wg.Wait()
}

func (w *Worker) consume(ctx context.Context, id int) {
	for work := range w.ch {
		w.process(ctx, work)
		_ = id
	}
}

func (w *Worker) process(ctx context.Context, work Work) {
	log := w.logger.With(
		"job_id", work.JobID,
		"installation_id", work.InstallationID,
		"issue_key", work.Request.IssueKey,
	)

	// Mark running. If we lost a race (row already failed by janitor), bail.
	if err := w.jobs.MarkRunning(ctx, work.JobID); err != nil {
		log.Warn("could not mark running; dropping work", "err", err)
		return
	}

	start := time.Now()
	llm := w.llm
	if work.Model == "heavy" && w.llmHeavy != nil {
		llm = w.llmHeavy
	}
	findings, err := analysis.Run(ctx, llm, work.Request)
	if err != nil {
		log.Error("llm run failed", "err", err, "duration_ms", time.Since(start).Milliseconds())
		_ = w.jobs.MarkFailed(context.Background(), work.JobID, classifyLLMError(err))
		return
	}

	// Validate every finding against the catalog *before* writing anything,
	// so a partial write can't leave us with un-renderable rows.
	for i, f := range findings {
		if _, resolveErr := w.cat.Resolve(f.MessageKey, f.Params, catalog.RoleDefault); resolveErr != nil {
			log.Error("finding failed catalog validation",
				"err", resolveErr,
				"index", i,
				"message_key", f.MessageKey,
			)
			_ = w.jobs.MarkFailed(context.Background(), work.JobID, "catalog_reject")
			return
		}
	}

	// Persist. Keep this sequential — the volume per job is low (≤ ~20).
	for _, f := range findings {
		if err := w.findings.Insert(ctx, work.JobID, store.FindingInput{
			Category:     f.Category,
			Severity:     f.Severity,
			Score:        f.Score,
			Anchor:       store.Anchor{Field: f.Anchor.Field, Start: f.Anchor.Start, End: f.Anchor.End},
			MessageKey:   f.MessageKey,
			Params:       f.Params,
			RationaleTag: f.RationaleTag,
		}); err != nil {
			log.Error("findings insert failed", "err", err)
			_ = w.jobs.MarkFailed(context.Background(), work.JobID, "db_write")
			return
		}
	}

	if err := w.jobs.MarkDone(ctx, work.JobID); err != nil {
		log.Error("mark done failed", "err", err)
		return
	}

	if err := w.audit.Log(ctx, work.InstallationID, "", "analysis.completed", work.Request.IssueKey, map[string]any{
		"job_id":         work.JobID,
		"findings_count": len(findings),
		"duration_ms":    time.Since(start).Milliseconds(),
	}); err != nil {
		log.Warn("audit log failed", "err", err)
	}

	log.Info("analysis complete",
		"findings", len(findings),
		"duration_ms", time.Since(start).Milliseconds(),
	)
}

func (w *Worker) sweepLoop(ctx context.Context) {
	t := time.NewTicker(w.sweepTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := w.jobs.SweepQueuedOlderThan(context.Background(), w.sweepAge)
			if err != nil {
				w.logger.Error("sweep failed", "err", err)
				continue
			}
			if n > 0 {
				w.logger.Info("sweep failed stale queued jobs", "count", n)
			}
		}
	}
}

// classifyLLMError maps an LLM error into a stable error code. Never stores
// the raw LLM error text.
func classifyLLMError(err error) string {
	// For now we have one code per broad category. Future work: inspect
	// anthropic-sdk-go error types (rate limit, timeout, parse) and map
	// them specifically.
	if errors.Is(err, context.DeadlineExceeded) {
		return "llm_timeout"
	}
	// analysis.Run returns parse failures wrapped with "analysis parse" —
	// but we deliberately don't match on that string (fragile); it lands
	// as llm_error. Tighten in follow-up when the SDK exposes typed errors.
	return "llm_error"
}
