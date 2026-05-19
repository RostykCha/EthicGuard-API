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

type fakeMetrics struct {
	count int
	err   error
	calls []metricsCall
}

type metricsCall struct {
	installationID int64
	projectKey     string
}

func (f *fakeMetrics) CountCoveredIssues(_ context.Context, installationID int64, projectKey string) (int, error) {
	f.calls = append(f.calls, metricsCall{installationID, projectKey})
	if f.err != nil {
		return 0, f.err
	}
	return f.count, nil
}

func newMetricsTestHandler(m MetricsRepo) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &MetricsHandler{Logger: logger, Metrics: m}
	mux := http.NewServeMux()
	mux.Handle("GET /v1/projects/{projectKey}/metrics", h)
	inst := &store.Installation{ID: 42, CloudID: "cloud-xyz"}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(auth.WithInstallationForTest(r.Context(), inst))
		mux.ServeHTTP(w, r)
	})
}

func TestMetricsHandler_Get_ReturnsCount(t *testing.T) {
	m := &fakeMetrics{count: 3}
	handler := newMetricsTestHandler(m)

	req := httptest.NewRequest(http.MethodGet, "/v1/projects/KAN/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var got projectMetricsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ProjectKey != "KAN" {
		t.Errorf("projectKey = %q, want KAN", got.ProjectKey)
	}
	if got.CoveredIssueCount != 3 {
		t.Errorf("coveredIssueCount = %d, want 3", got.CoveredIssueCount)
	}
	if len(m.calls) != 1 || m.calls[0].installationID != 42 || m.calls[0].projectKey != "KAN" {
		t.Errorf("calls = %+v", m.calls)
	}
}

func TestMetricsHandler_Get_ZeroForEmptyProject(t *testing.T) {
	// A project that has never been analyzed (or has no projects row at all)
	// returns 200 with count=0 — not 404. The card always renders.
	m := &fakeMetrics{count: 0}
	handler := newMetricsTestHandler(m)

	req := httptest.NewRequest(http.MethodGet, "/v1/projects/EMPTY/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got projectMetricsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.CoveredIssueCount != 0 {
		t.Errorf("coveredIssueCount = %d, want 0", got.CoveredIssueCount)
	}
}

func TestMetricsHandler_Get_InternalErrorOnDBFailure(t *testing.T) {
	m := &fakeMetrics{err: errors.New("db down")}
	handler := newMetricsTestHandler(m)

	req := httptest.NewRequest(http.MethodGet, "/v1/projects/KAN/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestMetricsHandler_MethodNotAllowed(t *testing.T) {
	handler := newMetricsTestHandler(&fakeMetrics{})

	req := httptest.NewRequest(http.MethodPost, "/v1/projects/KAN/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// The router itself rejects unregistered method+path pairs with 405.
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 405 or 404", rec.Code)
	}
}
