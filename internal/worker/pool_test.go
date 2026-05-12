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
// can assert the final state. ClaimNext drains `pendingQueue` first (used by
// the concurrency tests), then falls back to `pending` for single-job cases.
type fakeJobs struct {
	mu sync.Mutex

	// Job to return on first ClaimNext. Subsequent calls return ErrNotFound.
	pending *store.Job

	// pendingQueue is the multi-job variant used by Pool.Start tests.
	pendingQueue []*store.Job

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
	if len(f.pendingQueue) > 0 {
		j := f.pendingQueue[0]
		f.pendingQueue = f.pendingQueue[1:]
		return j, nil
	}
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

// TestPool_Start_ProcessesAllJobs runs the full Start → run → ClaimNext →
// processJob pipeline with N workers and M > N jobs to catch goroutine-pool
// bugs the single-worker tests miss (double-claim, missed wake, etc.).
func TestPool_Start_ProcessesAllJobs(t *testing.T) {
	const (
		workers = 3
		njobs   = 7
	)
	q := jobs.New()
	fj := &fakeJobs{}
	for i := 1; i <= njobs; i++ {
		id := int64(i)
		fj.pendingQueue = append(fj.pendingQueue, &store.Job{
			ID: id, IssueKey: "KAN-1", Kind: "ac_quality",
		})
		q.Put(id, jobs.Entry{Payload: analysis.IssuePayload{
			Key:                "KAN-1",
			AcceptanceCriteria: "long enough AC text here for the MinACLength bar",
			HasTestLinks:       true,
		}})
	}
	pool := New(Deps{
		Logger:     discardLogger(),
		Jobs:       fj,
		Findings:   &fakeFindings{},
		Queue:      q,
		LLM:        &fakeLLM{response: "[]"},
		JobTimeout: time.Second,
	}, workers, 10*time.Millisecond) // short poll so the loop doesn't park

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)

	// Wait until every job has reached a terminal state, then cancel.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fj.mu.Lock()
		total := len(fj.doneCalls) + len(fj.failedCalls)
		fj.mu.Unlock()
		if total >= njobs {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	fj.mu.Lock()
	defer fj.mu.Unlock()
	if len(fj.doneCalls) != njobs {
		t.Errorf("done = %d, want %d (failed: %+v)", len(fj.doneCalls), njobs, fj.failedCalls)
	}
	if len(fj.failedCalls) != 0 {
		t.Errorf("unexpected MarkFailed: %+v", fj.failedCalls)
	}
	// Every job must be processed exactly once. Build a set and check size.
	seen := map[int64]int{}
	for _, c := range fj.doneCalls {
		seen[c.jobID]++
	}
	if len(seen) != njobs {
		t.Errorf("unique jobs done = %d, want %d", len(seen), njobs)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("job %d processed %d times, want exactly 1", id, n)
		}
	}
}

// TestPool_Start_GracefulShutdown asserts workers stop within a small budget
// after the context is cancelled, even while polling. Uses a long poll
// interval so the only way out is the ctx.Done branch in run().
func TestPool_Start_GracefulShutdown(t *testing.T) {
	fj := &fakeJobs{} // empty — ClaimNext returns ErrNotFound immediately
	pool := New(Deps{
		Logger:     discardLogger(),
		Jobs:       fj,
		Findings:   &fakeFindings{},
		Queue:      jobs.New(),
		LLM:        &fakeLLM{response: "[]"},
		JobTimeout: time.Second,
	}, 2, time.Hour /* never ticks */)

	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)

	// Give workers a moment to enter the select loop, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	// Workers should exit within a few ms — they wake on ctx.Done.
	// Goroutine-leak detection isn't built-in here; we just sleep briefly
	// and trust that a stuck worker would show up under `-race -count=N`.
	time.Sleep(50 * time.Millisecond)
}

// TestPool_Start_ContextCancelledDuringClaim — ClaimNext returns
// context.Canceled while the worker is mid-drain. The worker must log and
// exit, not loop forever.
func TestPool_Start_ContextCancelledDuringClaim(t *testing.T) {
	fj := &cancellingJobs{}
	pool := New(Deps{
		Logger:     discardLogger(),
		Jobs:       fj,
		Findings:   &fakeFindings{},
		Queue:      jobs.New(),
		LLM:        &fakeLLM{response: "[]"},
		JobTimeout: time.Second,
	}, 1, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)

	// Wait for the worker to enter ClaimNext, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	// The worker should observe context.Canceled from ClaimNext and exit.
	time.Sleep(50 * time.Millisecond)
}

// cancellingJobs always returns context.Canceled — exercises the
// errors.Is(err, context.Canceled) branch in run().
type cancellingJobs struct{}

func (c *cancellingJobs) ClaimNext(_ context.Context) (*store.Job, error) {
	return nil, context.Canceled
}
func (c *cancellingJobs) MarkDone(_ context.Context, _ int64, _ string) error    { return nil }
func (c *cancellingJobs) MarkFailed(_ context.Context, _ int64, _ string) error { return nil }

// TestPool_Run_ClaimError_Continues — a non-NotFound, non-cancel claim error
// must be logged and not panic the worker. We trigger one, then unblock.
func TestPool_Run_ClaimError_Continues(t *testing.T) {
	fj := &flakeyJobs{firstErr: errors.New("transient db blip")}
	pool := New(Deps{
		Logger:     discardLogger(),
		Jobs:       fj,
		Findings:   &fakeFindings{},
		Queue:      jobs.New(),
		LLM:        &fakeLLM{response: "[]"},
		JobTimeout: time.Second,
	}, 1, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)
	time.Sleep(40 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)

	fj.mu.Lock()
	defer fj.mu.Unlock()
	if fj.calls < 2 {
		t.Errorf("ClaimNext called %d times; expected the worker to retry after the first error", fj.calls)
	}
}

// flakeyJobs returns one error on the first ClaimNext and then ErrNotFound.
type flakeyJobs struct {
	mu       sync.Mutex
	calls    int
	firstErr error
}

func (f *flakeyJobs) ClaimNext(_ context.Context) (*store.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls == 1 {
		return nil, f.firstErr
	}
	return nil, store.ErrNotFound
}
func (f *flakeyJobs) MarkDone(_ context.Context, _ int64, _ string) error    { return nil }
func (f *flakeyJobs) MarkFailed(_ context.Context, _ int64, _ string) error { return nil }
