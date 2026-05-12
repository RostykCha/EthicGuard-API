package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

type fakeJobsRepo struct {
	job       *store.Job
	getErr    error
	latest    *store.Job
	latestErr error
}

func (f *fakeJobsRepo) Enqueue(_ context.Context, _, _ int64, _, _, _ string) (int64, error) {
	return 0, errors.New("not used")
}

func (f *fakeJobsRepo) GetByID(_ context.Context, _, _ int64) (*store.Job, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.job, nil
}

func (f *fakeJobsRepo) LatestForIssue(_ context.Context, _ int64, _ string) (*store.Job, error) {
	if f.latestErr != nil {
		return nil, f.latestErr
	}
	return f.latest, nil
}

type fakeFindings struct {
	findings []store.PersistedFinding
	err      error
}

func (f *fakeFindings) ListByJob(_ context.Context, _ int64) ([]store.PersistedFinding, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.findings, nil
}

func newJobsTestHandler(j JobsRepo, f FindingsRepo) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &JobsHandler{Logger: logger, Jobs: j, Findings: f}
	mux := http.NewServeMux()
	mux.Handle("GET /v1/analysis/{jobId}", h)
	inst := &store.Installation{ID: 42, CloudID: "cloud-xyz"}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(auth.WithInstallationForTest(r.Context(), inst))
		mux.ServeHTTP(w, r)
	})
}

func newLatestTestHandler(j LatestJobLookup, byID JobsRepo, f FindingsRepo) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &LatestIssueHandler{Logger: logger, Jobs: j, JobByID: byID, Findings: f}
	mux := http.NewServeMux()
	mux.Handle("GET /v1/issues/{issueKey}/latest", h)
	inst := &store.Installation{ID: 42, CloudID: "cloud-xyz"}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(auth.WithInstallationForTest(r.Context(), inst))
		mux.ServeHTTP(w, r)
	})
}

func TestJobsHandler_Get_Happy_Queued(t *testing.T) {
	j := &fakeJobsRepo{job: &store.Job{
		ID: 7, IssueKey: "KAN-1", Status: store.JobQueued,
	}}
	// Queued status — findings must NOT be loaded.
	f := &fakeFindings{err: errors.New("should not be called")}
	handler := newJobsTestHandler(j, f)

	req := httptest.NewRequest(http.MethodGet, "/v1/analysis/7", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp jobResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.JobID != 7 || resp.Status != string(store.JobQueued) {
		t.Errorf("got %+v", resp)
	}
	if len(resp.Findings) != 0 {
		t.Errorf("findings should be empty for queued, got %d", len(resp.Findings))
	}
}

func TestJobsHandler_Get_Done_HydratesFindings(t *testing.T) {
	j := &fakeJobsRepo{job: &store.Job{
		ID: 7, IssueKey: "KAN-1", Status: store.JobDone, ResultLabel: "AC-verified",
	}}
	f := &fakeFindings{findings: []store.PersistedFinding{
		{Category: "ambiguity", Severity: "medium", Score: 60,
			Anchor: map[string]any{"field": "ac"}, MessageKey: "ambiguity.vague"},
	}}
	handler := newJobsTestHandler(j, f)

	req := httptest.NewRequest(http.MethodGet, "/v1/analysis/7", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp jobResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(resp.Findings))
	}
	if resp.Findings[0].MessageKey != "ambiguity.vague" {
		t.Errorf("messageKey = %q", resp.Findings[0].MessageKey)
	}
	// Message field is resolved from the catalog — non-empty even when the
	// key has no entry (catalog returns a fallback or the key itself).
	if resp.ResultLabel != "AC-verified" {
		t.Errorf("resultLabel = %q", resp.ResultLabel)
	}
}

func TestJobsHandler_Get_InvalidJobID(t *testing.T) {
	handler := newJobsTestHandler(&fakeJobsRepo{}, &fakeFindings{})
	req := httptest.NewRequest(http.MethodGet, "/v1/analysis/abc", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestJobsHandler_Get_NotFound(t *testing.T) {
	j := &fakeJobsRepo{getErr: store.ErrNotFound}
	handler := newJobsTestHandler(j, &fakeFindings{})
	req := httptest.NewRequest(http.MethodGet, "/v1/analysis/7", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestJobsHandler_Get_LookupError(t *testing.T) {
	j := &fakeJobsRepo{getErr: errors.New("db down")}
	handler := newJobsTestHandler(j, &fakeFindings{})
	req := httptest.NewRequest(http.MethodGet, "/v1/analysis/7", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestJobsHandler_Get_FindingsError(t *testing.T) {
	j := &fakeJobsRepo{job: &store.Job{ID: 7, Status: store.JobDone}}
	f := &fakeFindings{err: errors.New("db down")}
	handler := newJobsTestHandler(j, f)
	req := httptest.NewRequest(http.MethodGet, "/v1/analysis/7", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestJobsHandler_Get_NoInstallation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &JobsHandler{Logger: logger, Jobs: &fakeJobsRepo{}, Findings: &fakeFindings{}}
	req := httptest.NewRequest(http.MethodGet, "/v1/analysis/7", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestLatestIssue_Happy_Done(t *testing.T) {
	j := &fakeJobsRepo{latest: &store.Job{
		ID: 11, IssueKey: "KAN-1", Status: store.JobDone, ResultLabel: "AC-verified",
	}}
	f := &fakeFindings{}
	handler := newLatestTestHandler(j, j, f)

	req := httptest.NewRequest(http.MethodGet, "/v1/issues/KAN-1/latest", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp jobResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.JobID != 11 || resp.IssueKey != "KAN-1" {
		t.Errorf("got %+v", resp)
	}
}

func TestLatestIssue_NotFound(t *testing.T) {
	j := &fakeJobsRepo{latestErr: store.ErrNotFound}
	handler := newLatestTestHandler(j, j, &fakeFindings{})
	req := httptest.NewRequest(http.MethodGet, "/v1/issues/KAN-1/latest", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestLatestIssue_LookupError(t *testing.T) {
	j := &fakeJobsRepo{latestErr: errors.New("db down")}
	handler := newLatestTestHandler(j, j, &fakeFindings{})
	req := httptest.NewRequest(http.MethodGet, "/v1/issues/KAN-1/latest", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestLatestIssue_FindingsError(t *testing.T) {
	j := &fakeJobsRepo{latest: &store.Job{ID: 11, Status: store.JobDone}}
	f := &fakeFindings{err: errors.New("db down")}
	handler := newLatestTestHandler(j, j, f)
	req := httptest.NewRequest(http.MethodGet, "/v1/issues/KAN-1/latest", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestLatestIssue_NoInstallation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	j := &fakeJobsRepo{}
	h := &LatestIssueHandler{Logger: logger, Jobs: j, JobByID: j, Findings: &fakeFindings{}}
	req := httptest.NewRequest(http.MethodGet, "/v1/issues/KAN-1/latest", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}
