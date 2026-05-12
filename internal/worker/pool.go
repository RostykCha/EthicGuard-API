// Package worker runs queued analysis jobs against the LLM. Workers claim
// from Postgres via SELECT ... FOR UPDATE SKIP LOCKED, look up the issue
// payload in the in-process Queue, run analysis, persist findings + label
// decision, and update job status. See ../jobs for the payload-bus rationale.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ethicguard/ethicguard-api/internal/analysis"
	"github.com/ethicguard/ethicguard-api/internal/jobs"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

// Deps bundles the collaborators a worker pool needs.
type Deps struct {
	Logger   *slog.Logger
	Jobs     *store.Jobs
	Findings *store.Findings
	Queue    *jobs.Queue
	LLM      analysis.LLM
}

// Pool is a small fixed-size goroutine pool. Concurrency comes from launching
// N workers, not from any one worker being clever — keep each worker boring.
type Pool struct {
	deps        Deps
	concurrency int
	pollEvery   time.Duration
}

// New builds a Pool with the given concurrency (>=1) and poll interval.
// Default tick (5s) is a safety net for missed wake signals — actual latency
// is dominated by Queue.Wake firing on Put.
func New(deps Deps, concurrency int, pollEvery time.Duration) *Pool {
	if concurrency < 1 {
		concurrency = 1
	}
	if pollEvery <= 0 {
		pollEvery = 5 * time.Second
	}
	return &Pool{deps: deps, concurrency: concurrency, pollEvery: pollEvery}
}

// Start spawns workers and returns. They run until ctx is cancelled.
func (p *Pool) Start(ctx context.Context) {
	for i := 0; i < p.concurrency; i++ {
		i := i
		go p.run(ctx, i)
	}
}

func (p *Pool) run(ctx context.Context, id int) {
	log := p.deps.Logger.With("worker_id", id, "component", "worker")
	log.Info("worker started")
	ticker := time.NewTicker(p.pollEvery)
	defer ticker.Stop()

	for {
		// Drain any backlog as long as ClaimNext keeps returning work.
		for {
			job, err := p.deps.Jobs.ClaimNext(ctx)
			if err != nil {
				if store.IsNotFound(err) {
					break // nothing queued
				}
				if errors.Is(err, context.Canceled) {
					log.Info("worker stopping")
					return
				}
				log.Error("claim failed", "err", err)
				break
			}
			p.processJob(ctx, log, job)
		}
		// Then wait for a wake signal, a tick, or shutdown.
		select {
		case <-ctx.Done():
			log.Info("worker stopping")
			return
		case <-p.deps.Queue.Wake():
		case <-ticker.C:
		}
	}
}

func (p *Pool) processJob(ctx context.Context, log *slog.Logger, job *store.Job) {
	log = log.With("job_id", job.ID, "issue_key", job.IssueKey, "kind", job.Kind)

	entry, ok := p.deps.Queue.Take(job.ID)
	if !ok {
		log.Warn("payload missing — orphaned job, marking failed")
		if err := p.deps.Jobs.MarkFailed(ctx, job.ID, "orphaned"); err != nil {
			log.Error("mark failed (orphaned) failed", "err", err)
		}
		return
	}
	payload := entry.Payload

	start := time.Now()
	req := &analysis.AnalysisRequest{
		IssueKey:   job.IssueKey,
		ProjectKey: "", // not used by Run; kind/payload carry what matters
		Kind:       job.Kind,
		Payload:    payload,
	}
	resp, err := analysis.Run(ctx, p.deps.LLM, req, entry.Options)
	duration := time.Since(start)

	if err != nil {
		log.Error("analysis failed", "err", err, "duration_ms", duration.Milliseconds())
		if err := p.deps.Jobs.MarkFailed(ctx, job.ID, "llm_error"); err != nil {
			log.Error("mark failed failed", "err", err)
		}
		return
	}

	persisted := make([]store.PersistedFinding, 0, len(resp.Findings))
	for _, f := range resp.Findings {
		persisted = append(persisted, store.PersistedFinding{
			JobID:      job.ID,
			Category:   f.Category,
			Severity:   f.Severity,
			Score:      f.Score,
			Anchor:     map[string]any{"field": f.Anchor.Field},
			MessageKey: analysis.MessageKey(f.Category, f.Severity),
		})
	}
	if err := p.deps.Findings.InsertBatch(ctx, job.ID, persisted); err != nil {
		log.Error("persist findings failed", "err", err)
		if err := p.deps.Jobs.MarkFailed(ctx, job.ID, "persist_findings"); err != nil {
			log.Error("mark failed failed", "err", err)
		}
		return
	}

	decision := analysis.Decide(resp.Findings, &payload)
	if err := p.deps.Jobs.MarkDone(ctx, job.ID, decision.Primary); err != nil {
		log.Error("mark done failed", "err", err)
		return
	}
	log.Info("analysis complete",
		"findings", len(resp.Findings),
		"label", decision.Primary,
		"no_test", decision.NoTest,
		"duration_ms", duration.Milliseconds(),
	)
}
