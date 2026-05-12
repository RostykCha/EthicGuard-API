package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ethicguard/ethicguard-api/internal/analysis"
	"github.com/ethicguard/ethicguard-api/internal/jobs"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

// fakeJobs implements JobsRepo, capturing MarkDone/MarkFailed calls so tests
// can assert the final state. ClaimNext returns one job then ErrNotFound.
type fakeJobs struct {
	mu sync.Mutex

	// Job to return on first ClaimNext. Subsequent calls return ErrNotFound.
	pending *store.Job

	doneCalls   []doneCall
	failedCalls []failedCall

	// markDoneErr / markFailedErr let tests force a persistence failure.
	markDoneErr   error
	markFailedErr error
}

type doneCall struct {
	jobID int64
	label string
}

type failedCall struct {
	jobID int64
	code  string
}

func (f *fakeJobs) ClaimNext(_ context.Context) (*store.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending == nil {
		return nil, store.ErrNotFound
	}
	j := f.pending
	f.pending = nil
	return j, nil
}

func (f *fakeJobs) MarkDone(_ context.Context, jobID int64, resultLabel string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.doneCalls = append(f.doneCalls, doneCall{jobID, resultLabel})
	return f.markDoneErr
}

func (f *fakeJobs) MarkFailed(_ context.Context, jobID int64, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failedCalls = append(f.failedCalls, failedCall{jobID, code})
	return f.markFailedErr
}

// fakeFindings implements FindingsRepo.
type fakeFindings struct {
	mu        sync.Mutex
	inserted  [][]store.PersistedFinding
	insertErr error
}

func (f *fakeFindings) InsertBatch(_ context.Context, _ int64, findings []store.PersistedFinding) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inserted = append(f.inserted, findings)
	return f.insertErr
}

// fakeLLM implements analysis.LLM. Response and behavior are scripted per
// test — either return the canned response, return an error, or block until
// the context is cancelled (to exercise the timeout path).
type fakeLLM struct {
	response string
	err      error
	block    bool
}

func (f *fakeLLM) Analyze(ctx context.Context, _, _, _ string) (string, error) {
	if f.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return f.response, f.err
}

func newPool(deps Deps) *Pool {
	return New(deps, 1, time.Hour /* never tick during a test */)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// makeJob returns a queued Job suitable for processJob.
func makeJob() *store.Job {
	return &store.Job{
		ID:             1,
		InstallationID: 100,
		ProjectID:      200,
		IssueKey:       "KAN-1",
		Kind:           "ac_quality",
		Status:         store.JobQueued,
	}
}

func TestProcessJob_HappyPath(t *testing.T) {
	q := jobs.New()
	q.Put(1, jobs.Entry{
		Payload: analysis.IssuePayload{
			Key:                "KAN-1",
			AcceptanceCriteria: "Given X when Y then Z. This AC is long enough to clear the MinACLength bar comfortably.",
			HasTestLinks:       true,
		},
	})

	fj := &fakeJobs{}
	ff := &fakeFindings{}
	pool := newPool(Deps{
		Logger:     discardLogger(),
		Jobs:       fj,
		Findings:   ff,
		Queue:      q,
		LLM:        &fakeLLM{response: "[]"}, // no findings
		JobTimeout: time.Second,
	})

	pool.processJob(context.Background(), discardLogger(), makeJob())

	if len(fj.doneCalls) != 1 {
		t.Fatalf("MarkDone calls = %d, want 1; failed = %+v", len(fj.doneCalls), fj.failedCalls)
	}
	if fj.doneCalls[0].label != analysis.LabelACVerified {
		t.Errorf("label = %q, want %q", fj.doneCalls[0].label, analysis.LabelACVerified)
	}
	if len(fj.failedCalls) != 0 {
		t.Errorf("unexpected MarkFailed: %+v", fj.failedCalls)
	}
	if len(ff.inserted) != 1 || len(ff.inserted[0]) != 0 {
		t.Errorf("findings InsertBatch = %+v, want one empty slice", ff.inserted)
	}
}

func TestProcessJob_OrphanedPayload(t *testing.T) {
	q := jobs.New() // no Put — payload missing
	fj := &fakeJobs{}
	pool := newPool(Deps{
		Logger:     discardLogger(),
		Jobs:       fj,
		Findings:   &fakeFindings{},
		Queue:      q,
		LLM:        &fakeLLM{response: "[]"},
		JobTimeout: time.Second,
	})

	pool.processJob(context.Background(), discardLogger(), makeJob())

	if len(fj.failedCalls) != 1 {
		t.Fatalf("MarkFailed calls = %d, want 1", len(fj.failedCalls))
	}
	if fj.failedCalls[0].code != "orphaned" {
		t.Errorf("code = %q, want orphaned", fj.failedCalls[0].code)
	}
}

func TestProcessJob_LLMError(t *testing.T) {
	q := jobs.New()
	q.Put(1, jobs.Entry{Payload: analysis.IssuePayload{Key: "KAN-1"}})

	fj := &fakeJobs{}
	pool := newPool(Deps{
		Logger:     discardLogger(),
		Jobs:       fj,
		Findings:   &fakeFindings{},
		Queue:      q,
		LLM:        &fakeLLM{err: errors.New("network blew up")},
		JobTimeout: time.Second,
	})

	pool.processJob(context.Background(), discardLogger(), makeJob())

	if len(fj.failedCalls) != 1 || fj.failedCalls[0].code != "llm_error" {
		t.Errorf("expected one MarkFailed(llm_error), got %+v", fj.failedCalls)
	}
}

func TestProcessJob_Timeout(t *testing.T) {
	q := jobs.New()
	q.Put(1, jobs.Entry{
		Payload: analysis.IssuePayload{
			Key:                "KAN-1",
			AcceptanceCriteria: "long enough AC text here for the MinACLength bar",
		},
	})

	fj := &fakeJobs{}
	pool := newPool(Deps{
		Logger:     discardLogger(),
		Jobs:       fj,
		Findings:   &fakeFindings{},
		Queue:      q,
		LLM:        &fakeLLM{block: true}, // hangs until ctx cancels
		JobTimeout: 25 * time.Millisecond, // fires fast
	})

	start := time.Now()
	pool.processJob(context.Background(), discardLogger(), makeJob())
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Fatalf("processJob took %v, expected fast timeout", elapsed)
	}
	if len(fj.failedCalls) != 1 {
		t.Fatalf("MarkFailed calls = %d, want 1", len(fj.failedCalls))
	}
	if fj.failedCalls[0].code != "timeout" {
		t.Errorf("code = %q, want timeout", fj.failedCalls[0].code)
	}
}

func TestProcessJob_PersistFindingsError(t *testing.T) {
	q := jobs.New()
	q.Put(1, jobs.Entry{
		Payload: analysis.IssuePayload{
			Key:                "KAN-1",
			AcceptanceCriteria: "long enough AC text here for the MinACLength bar",
		},
	})

	fj := &fakeJobs{}
	ff := &fakeFindings{insertErr: errors.New("db down")}
	pool := newPool(Deps{
		Logger:     discardLogger(),
		Jobs:       fj,
		Findings:   ff,
		Queue:      q,
		LLM:        &fakeLLM{response: "[]"},
		JobTimeout: time.Second,
	})

	pool.processJob(context.Background(), discardLogger(), makeJob())

	if len(fj.failedCalls) != 1 || fj.failedCalls[0].code != "persist_findings" {
		t.Errorf("expected MarkFailed(persist_findings), got %+v", fj.failedCalls)
	}
	if len(fj.doneCalls) != 0 {
		t.Errorf("unexpected MarkDone after persist failure: %+v", fj.doneCalls)
	}
}

func TestPoolNew_DefaultsJobTimeout(t *testing.T) {
	pool := New(Deps{}, 0, 0)
	if pool.deps.JobTimeout != defaultJobTimeout {
		t.Errorf("JobTimeout = %v, want %v", pool.deps.JobTimeout, defaultJobTimeout)
	}
	if pool.concurrency != 1 {
		t.Errorf("concurrency = %d, want 1 (min)", pool.concurrency)
	}
}
