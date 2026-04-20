package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ethicguard/ethicguard-api/internal/analysis"
	"github.com/ethicguard/ethicguard-api/internal/catalog"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

// stubLLM returns whatever raw text the test supplied, ignoring the prompt.
type stubLLM struct {
	raw string
	err error
}

func (s *stubLLM) Analyze(_ context.Context, _, _ string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.raw, nil
}

type fakeJobs struct {
	mu       sync.Mutex
	running  map[int64]bool
	done     map[int64]bool
	failed   map[int64]string
}

func newFakeJobs() *fakeJobs {
	return &fakeJobs{
		running: map[int64]bool{},
		done:    map[int64]bool{},
		failed:  map[int64]string{},
	}
}

func (f *fakeJobs) MarkRunning(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running[id] = true
	return nil
}

func (f *fakeJobs) MarkDone(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.done[id] = true
	return nil
}

func (f *fakeJobs) MarkFailed(_ context.Context, id int64, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed[id] = code
	return nil
}

func (f *fakeJobs) SweepQueuedOlderThan(_ context.Context, _ time.Duration) (int64, error) {
	return 0, nil
}

type fakeFindings struct {
	mu   sync.Mutex
	rows []insertRecord
}

type insertRecord struct {
	jobID int64
	in    store.FindingInput
}

func (f *fakeFindings) Insert(_ context.Context, jobID int64, in store.FindingInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, insertRecord{jobID: jobID, in: in})
	return nil
}

type fakeAudit struct {
	mu      sync.Mutex
	entries int
}

func (f *fakeAudit) Log(_ context.Context, _ int64, _, _, _ string, _ map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries++
	return nil
}

func newSilentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestWorker_HappyPath(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}

	jobsRepo := newFakeJobs()
	findingsRepo := &fakeFindings{}
	audit := &fakeAudit{}
	llm := &stubLLM{raw: `[
		{"category":"ambiguity","severity":"medium","score":50,
		 "anchor":{"field":"description"},
		 "messageKey":"ambiguity.vague_quantifier",
		 "params":{"field":"description","term":"several"}}
	]`}

	w := New(newSilentLogger(), Config{PoolSize: 1, Buffer: 4}, jobsRepo, findingsRepo, audit, llm, cat)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	work := Work{
		JobID:          7,
		InstallationID: 1,
		Request: &analysis.AnalysisRequest{
			IssueKey:   "PROJ-1",
			ProjectKey: "PROJ",
			Kind:       "ac_quality",
			Payload:    analysis.IssuePayload{Key: "PROJ-1", Description: "several users"},
		},
	}
	if err := w.Dispatch(ctx, work); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	waitFor(t, func() bool {
		jobsRepo.mu.Lock()
		defer jobsRepo.mu.Unlock()
		return jobsRepo.done[7]
	}, 2*time.Second)

	cancel()
	<-done

	if got := len(findingsRepo.rows); got != 1 {
		t.Fatalf("want 1 finding inserted, got %d", got)
	}
	if got := findingsRepo.rows[0].in.MessageKey; got != "ambiguity.vague_quantifier" {
		t.Fatalf("unexpected message key %q", got)
	}
	if findingsRepo.rows[0].in.Params["field"] != "description" {
		t.Fatalf("params not persisted: %+v", findingsRepo.rows[0].in.Params)
	}
	if audit.entries != 1 {
		t.Fatalf("want 1 audit entry, got %d", audit.entries)
	}
}

func TestWorker_CatalogRejectFailsJob(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	jobsRepo := newFakeJobs()
	findingsRepo := &fakeFindings{}
	audit := &fakeAudit{}
	// LLM emits a key that does not exist in the catalog — worker must fail
	// the job without writing any findings.
	llm := &stubLLM{raw: `[
		{"category":"ambiguity","severity":"low","score":10,
		 "anchor":{"field":"description"},
		 "messageKey":"does.not.exist","params":{}}
	]`}
	w := New(newSilentLogger(), Config{PoolSize: 1, Buffer: 2}, jobsRepo, findingsRepo, audit, llm, cat)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	_ = w.Dispatch(ctx, Work{
		JobID: 9,
		Request: &analysis.AnalysisRequest{
			IssueKey: "PROJ-9", ProjectKey: "PROJ", Kind: "ac_quality",
			Payload: analysis.IssuePayload{Key: "PROJ-9"},
		},
	})

	waitFor(t, func() bool {
		jobsRepo.mu.Lock()
		defer jobsRepo.mu.Unlock()
		return jobsRepo.failed[9] != ""
	}, 2*time.Second)
	cancel()
	<-done

	if code := jobsRepo.failed[9]; code != "catalog_reject" {
		t.Fatalf("want failure code catalog_reject, got %q", code)
	}
	if len(findingsRepo.rows) != 0 {
		t.Fatalf("no findings should be written on catalog reject, got %d", len(findingsRepo.rows))
	}
}

func TestWorker_LLMErrorMarksFailed(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	jobsRepo := newFakeJobs()
	findingsRepo := &fakeFindings{}
	audit := &fakeAudit{}
	llm := &stubLLM{err: errors.New("boom")}
	w := New(newSilentLogger(), Config{PoolSize: 1, Buffer: 2}, jobsRepo, findingsRepo, audit, llm, cat)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	_ = w.Dispatch(ctx, Work{
		JobID:   3,
		Request: &analysis.AnalysisRequest{IssueKey: "X-1", ProjectKey: "X", Payload: analysis.IssuePayload{Key: "X-1"}},
	})
	waitFor(t, func() bool {
		jobsRepo.mu.Lock()
		defer jobsRepo.mu.Unlock()
		return jobsRepo.failed[3] != ""
	}, 2*time.Second)
	cancel()
	<-done
	if code := jobsRepo.failed[3]; code == "" {
		t.Fatalf("expected failure code, got empty")
	}
}

func TestDispatch_BusyReturnsErr(t *testing.T) {
	cat, _ := catalog.Load()
	w := New(newSilentLogger(), Config{PoolSize: 1, Buffer: 1}, newFakeJobs(), &fakeFindings{}, &fakeAudit{}, &stubLLM{raw: "[]"}, cat)
	// Fill the channel without starting Run so Dispatch sees it as full.
	if err := w.Dispatch(context.Background(), Work{JobID: 1}); err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}
	if err := w.Dispatch(context.Background(), Work{JobID: 2}); !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}
