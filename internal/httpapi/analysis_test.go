package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethicguard/ethicguard-api/internal/analysis"
	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/jobs"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

type fakeJobs struct {
	enqueueCalls int
	enqueueErr   error
	lastIssueKey string
	lastKind     string
	lastActor    string
	nextID       int64
}

func (f *fakeJobs) Enqueue(_ context.Context, _, _ int64, issueKey, kind, actor string) (int64, error) {
	f.enqueueCalls++
	f.lastIssueKey = issueKey
	f.lastKind = kind
	f.lastActor = actor
	if f.enqueueErr != nil {
		return 0, f.enqueueErr
	}
	if f.nextID == 0 {
		f.nextID = 1
	}
	id := f.nextID
	f.nextID++
	return id, nil
}

func (f *fakeJobs) GetByID(_ context.Context, _, _ int64) (*store.Job, error) {
	return nil, store.ErrNotFound
}

type fakeProjectsFull struct {
	*fakeProjects
	upsertID  int64
	upsertErr error
}

func (f *fakeProjectsFull) Upsert(_ context.Context, _ int64, _ string) (int64, error) {
	if f.upsertErr != nil {
		return 0, f.upsertErr
	}
	if f.upsertID == 0 {
		return 99, nil
	}
	return f.upsertID, nil
}

type fakeQueue struct {
	puts []putCall
}

type putCall struct {
	jobID int64
	entry jobs.Entry
}

func (f *fakeQueue) Put(jobID int64, e jobs.Entry) {
	f.puts = append(f.puts, putCall{jobID, e})
}

func newAnalysisHandler(p ProjectsRepoFull, j JobsRepo, q PayloadEnqueuer) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &AnalysisHandler{Logger: logger, Jobs: j, Projects: p, Queue: q}
	inst := &store.Installation{ID: 42, CloudID: "cloud-xyz"}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(auth.WithInstallationForTest(r.Context(), inst))
		h.ServeHTTP(w, r)
	})
}

func validRequest() analysis.AnalysisRequest {
	return analysis.AnalysisRequest{
		IssueKey:   "KAN-1",
		ProjectKey: "KAN",
		Kind:       "ac_quality",
		Payload: analysis.IssuePayload{
			Key:         "KAN-1",
			IssueTypeID: "10001",
		},
	}
}

func postAnalysis(handler http.Handler, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/analysis", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAnalysis_HappyPath(t *testing.T) {
	fp := newFakeProjects()
	fp.stored["KAN"] = &store.ProjectConfig{
		ProjectKey:             "KAN",
		TestedIssueTypes:       []string{"10001"},
		AgentEnabled:           true,
		AgentSeverityThreshold: "medium",
	}
	p := &fakeProjectsFull{fakeProjects: fp, upsertID: 7}
	j := &fakeJobs{}
	q := &fakeQueue{}
	handler := newAnalysisHandler(p, j, q)

	rec := postAnalysis(handler, validRequest())

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp enqueueResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Status != string(store.JobQueued) {
		t.Errorf("status = %q, want queued", resp.Status)
	}
	if resp.JobID == 0 {
		t.Errorf("jobID = 0, want > 0")
	}
	if j.enqueueCalls != 1 {
		t.Errorf("enqueue calls = %d, want 1", j.enqueueCalls)
	}
	if len(q.puts) != 1 {
		t.Fatalf("queue puts = %d, want 1", len(q.puts))
	}
	// Per-project severity threshold must flow through to the worker.
	if q.puts[0].entry.Options.SeverityThreshold != "medium" {
		t.Errorf("threshold = %q, want medium", q.puts[0].entry.Options.SeverityThreshold)
	}
}

func TestAnalysis_DefaultsKindWhenMissing(t *testing.T) {
	fp := newFakeProjects()
	fp.stored["KAN"] = &store.ProjectConfig{
		ProjectKey: "KAN", TestedIssueTypes: []string{"10001"}, AgentEnabled: true,
	}
	p := &fakeProjectsFull{fakeProjects: fp}
	j := &fakeJobs{}
	handler := newAnalysisHandler(p, j, &fakeQueue{})

	req := validRequest()
	req.Kind = ""
	rec := postAnalysis(handler, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if j.lastKind != "ac_quality" {
		t.Errorf("kind = %q, want ac_quality default", j.lastKind)
	}
}

func TestAnalysis_PassesActorHeader(t *testing.T) {
	fp := newFakeProjects()
	fp.stored["KAN"] = &store.ProjectConfig{
		ProjectKey: "KAN", TestedIssueTypes: []string{"10001"}, AgentEnabled: true,
	}
	p := &fakeProjectsFull{fakeProjects: fp}
	j := &fakeJobs{}
	handler := newAnalysisHandler(p, j, &fakeQueue{})

	b, _ := json.Marshal(validRequest())
	req := httptest.NewRequest(http.MethodPost, "/v1/analysis", bytes.NewReader(b))
	req.Header.Set("X-EthicGuard-Actor", "acct-99")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d", rec.Code)
	}
	if j.lastActor != "acct-99" {
		t.Errorf("actor = %q, want acct-99", j.lastActor)
	}
}

func TestAnalysis_InvalidJSON(t *testing.T) {
	handler := newAnalysisHandler(&fakeProjectsFull{fakeProjects: newFakeProjects()}, &fakeJobs{}, &fakeQueue{})
	req := httptest.NewRequest(http.MethodPost, "/v1/analysis", bytes.NewReader([]byte("{not json")))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAnalysis_MissingRequiredFields(t *testing.T) {
	handler := newAnalysisHandler(&fakeProjectsFull{fakeProjects: newFakeProjects()}, &fakeJobs{}, &fakeQueue{})
	// Missing issueKey.
	req := validRequest()
	req.IssueKey = ""
	rec := postAnalysis(handler, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAnalysis_ProjectNotConfigured(t *testing.T) {
	// GetConfig returns NotFound — handler returns 403 issue_type_out_of_scope.
	p := &fakeProjectsFull{fakeProjects: newFakeProjects()}
	handler := newAnalysisHandler(p, &fakeJobs{}, &fakeQueue{})
	rec := postAnalysis(handler, validRequest())
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestAnalysis_AgentDisabled(t *testing.T) {
	fp := newFakeProjects()
	fp.stored["KAN"] = &store.ProjectConfig{
		ProjectKey: "KAN", TestedIssueTypes: []string{"10001"}, AgentEnabled: false,
	}
	p := &fakeProjectsFull{fakeProjects: fp}
	handler := newAnalysisHandler(p, &fakeJobs{}, &fakeQueue{})
	rec := postAnalysis(handler, validRequest())
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	// Code should be 'agent_disabled' (not 'issue_type_out_of_scope').
	if !bytes.Contains(rec.Body.Bytes(), []byte("agent_disabled")) {
		t.Errorf("body should contain agent_disabled code: %s", rec.Body.String())
	}
}

func TestAnalysis_IssueTypeOutOfScope(t *testing.T) {
	fp := newFakeProjects()
	fp.stored["KAN"] = &store.ProjectConfig{
		ProjectKey: "KAN", TestedIssueTypes: []string{"99999"}, AgentEnabled: true,
	}
	p := &fakeProjectsFull{fakeProjects: fp}
	handler := newAnalysisHandler(p, &fakeJobs{}, &fakeQueue{})

	rec := postAnalysis(handler, validRequest()) // payload has 10001
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestAnalysis_UpsertFailure(t *testing.T) {
	fp := newFakeProjects()
	fp.stored["KAN"] = &store.ProjectConfig{
		ProjectKey: "KAN", TestedIssueTypes: []string{"10001"}, AgentEnabled: true,
	}
	p := &fakeProjectsFull{fakeProjects: fp, upsertErr: errors.New("db down")}
	handler := newAnalysisHandler(p, &fakeJobs{}, &fakeQueue{})

	rec := postAnalysis(handler, validRequest())
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestAnalysis_EnqueueFailure(t *testing.T) {
	fp := newFakeProjects()
	fp.stored["KAN"] = &store.ProjectConfig{
		ProjectKey: "KAN", TestedIssueTypes: []string{"10001"}, AgentEnabled: true,
	}
	p := &fakeProjectsFull{fakeProjects: fp}
	j := &fakeJobs{enqueueErr: errors.New("queue full")}
	handler := newAnalysisHandler(p, j, &fakeQueue{})

	rec := postAnalysis(handler, validRequest())
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestAnalysis_NoInstallationInContext(t *testing.T) {
	// Bypass the test wrapper: hit the handler directly with a context that
	// has no installation attached.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &AnalysisHandler{Logger: logger, Jobs: &fakeJobs{},
		Projects: &fakeProjectsFull{fakeProjects: newFakeProjects()}, Queue: &fakeQueue{}}
	b, _ := json.Marshal(validRequest())
	req := httptest.NewRequest(http.MethodPost, "/v1/analysis", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// AnalysisResultsHandler — POST /v1/analysis/results (Rovo-stamped verdicts)
// ---------------------------------------------------------------------------

type fakeResultsJobs struct {
	called     int
	lastIssue  string
	lastLabel  string
	recordErr  error
	returnedID int64
}

func (f *fakeResultsJobs) RecordCompleted(_ context.Context, _, _ int64, issueKey, _, label string) (int64, error) {
	f.called++
	f.lastIssue = issueKey
	f.lastLabel = label
	if f.recordErr != nil {
		return 0, f.recordErr
	}
	if f.returnedID == 0 {
		return 101, nil
	}
	return f.returnedID, nil
}

type fakeFindingsWriter struct {
	inserts   int
	lastJobID int64
	lastBatch []store.PersistedFinding
	insertErr error
}

func (f *fakeFindingsWriter) InsertBatch(_ context.Context, jobID int64, findings []store.PersistedFinding) error {
	f.inserts++
	f.lastJobID = jobID
	f.lastBatch = findings
	return f.insertErr
}

func newResultsHandler(p ProjectsRepoFull, j ResultsJobsRepo, f PersistedFindingsRepo) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &AnalysisResultsHandler{Logger: logger, Jobs: j, Projects: p, Findings: f}
	inst := &store.Installation{ID: 42, CloudID: "cloud-xyz"}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(auth.WithInstallationForTest(r.Context(), inst))
		h.ServeHTTP(w, r)
	})
}

func validResultsRequest() analysisResultsRequest {
	return analysisResultsRequest{
		IssueKey:   "KAN-1",
		ProjectKey: "KAN",
		Kind:       "ac_quality",
		Label:      "AC-verified",
		Findings: []analysisResultsFinding{
			{
				Category:   "ambiguity",
				Severity:   "medium",
				Score:      60,
				Anchor:     map[string]any{"field": "ac"},
				MessageKey: "ambiguity.vague",
			},
		},
	}
}

func postResults(handler http.Handler, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/analysis/results", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAnalysisResults_HappyPath(t *testing.T) {
	p := &fakeProjectsFull{fakeProjects: newFakeProjects(), upsertID: 7}
	j := &fakeResultsJobs{returnedID: 101}
	f := &fakeFindingsWriter{}
	handler := newResultsHandler(p, j, f)

	rec := postResults(handler, validResultsRequest())

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if j.called != 1 || j.lastIssue != "KAN-1" || j.lastLabel != "AC-verified" {
		t.Errorf("RecordCompleted call shape wrong: %+v", j)
	}
	if f.inserts != 1 || f.lastJobID != 101 || len(f.lastBatch) != 1 {
		t.Errorf("InsertBatch call shape wrong: %+v", f)
	}
	if f.lastBatch[0].JobID != 101 || f.lastBatch[0].MessageKey != "ambiguity.vague" {
		t.Errorf("persisted finding wrong: %+v", f.lastBatch[0])
	}
}

func TestAnalysisResults_RejectsFreeTextMessage(t *testing.T) {
	// Zero-retention defense-in-depth: a finding carrying free-text `message`
	// is rejected at the boundary. This is the only test that asserts the
	// rule by name — keep it explicit so a future refactor doesn't quietly
	// loosen the guard.
	req := validResultsRequest()
	req.Findings[0].Message = "Vague AC text on KAN-1"
	handler := newResultsHandler(
		&fakeProjectsFull{fakeProjects: newFakeProjects()},
		&fakeResultsJobs{},
		&fakeFindingsWriter{},
	)

	rec := postResults(handler, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("zero-retention")) {
		t.Errorf("body should mention zero-retention rule: %s", rec.Body.String())
	}
}

func TestAnalysisResults_InvalidJSON(t *testing.T) {
	handler := newResultsHandler(
		&fakeProjectsFull{fakeProjects: newFakeProjects()},
		&fakeResultsJobs{},
		&fakeFindingsWriter{},
	)
	req := httptest.NewRequest(http.MethodPost, "/v1/analysis/results",
		bytes.NewReader([]byte("{not json")))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAnalysisResults_MissingRequiredFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*analysisResultsRequest)
	}{
		{"issueKey", func(r *analysisResultsRequest) { r.IssueKey = "" }},
		{"projectKey", func(r *analysisResultsRequest) { r.ProjectKey = "" }},
		{"kind", func(r *analysisResultsRequest) { r.Kind = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := validResultsRequest()
			tc.mutate(&req)
			handler := newResultsHandler(
				&fakeProjectsFull{fakeProjects: newFakeProjects()},
				&fakeResultsJobs{},
				&fakeFindingsWriter{},
			)
			rec := postResults(handler, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestAnalysisResults_InvalidLabel(t *testing.T) {
	req := validResultsRequest()
	req.Label = "totally-made-up"
	handler := newResultsHandler(
		&fakeProjectsFull{fakeProjects: newFakeProjects()},
		&fakeResultsJobs{},
		&fakeFindingsWriter{},
	)
	rec := postResults(handler, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAnalysisResults_InvalidSeverity(t *testing.T) {
	req := validResultsRequest()
	req.Findings[0].Severity = "critical"
	handler := newResultsHandler(
		&fakeProjectsFull{fakeProjects: newFakeProjects()},
		&fakeResultsJobs{},
		&fakeFindingsWriter{},
	)
	rec := postResults(handler, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAnalysisResults_TooManyFindings(t *testing.T) {
	req := validResultsRequest()
	req.Findings = make([]analysisResultsFinding, maxFindingsPerResult+1)
	for i := range req.Findings {
		req.Findings[i] = analysisResultsFinding{
			Category: "c", Severity: "low", MessageKey: "k",
		}
	}
	handler := newResultsHandler(
		&fakeProjectsFull{fakeProjects: newFakeProjects()},
		&fakeResultsJobs{},
		&fakeFindingsWriter{},
	)
	rec := postResults(handler, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAnalysisResults_EmptyFindings_OK(t *testing.T) {
	// "AC-verified with no findings" is a legitimate verdict — the analyzer
	// can find nothing wrong. Must NOT 400 on empty findings.
	req := validResultsRequest()
	req.Findings = nil
	j := &fakeResultsJobs{returnedID: 1}
	f := &fakeFindingsWriter{}
	handler := newResultsHandler(
		&fakeProjectsFull{fakeProjects: newFakeProjects()},
		j,
		f,
	)
	rec := postResults(handler, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201, body=%s", rec.Code, rec.Body.String())
	}
	if f.inserts != 1 {
		t.Errorf("InsertBatch should still be called (no-op on empty); got %d calls", f.inserts)
	}
}

func TestAnalysisResults_UpsertFailure(t *testing.T) {
	p := &fakeProjectsFull{fakeProjects: newFakeProjects(), upsertErr: errors.New("db down")}
	handler := newResultsHandler(p, &fakeResultsJobs{}, &fakeFindingsWriter{})
	rec := postResults(handler, validResultsRequest())
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestAnalysisResults_RecordFailure(t *testing.T) {
	handler := newResultsHandler(
		&fakeProjectsFull{fakeProjects: newFakeProjects()},
		&fakeResultsJobs{recordErr: errors.New("db down")},
		&fakeFindingsWriter{},
	)
	rec := postResults(handler, validResultsRequest())
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestAnalysisResults_FindingsInsertFailure(t *testing.T) {
	handler := newResultsHandler(
		&fakeProjectsFull{fakeProjects: newFakeProjects()},
		&fakeResultsJobs{returnedID: 5},
		&fakeFindingsWriter{insertErr: errors.New("db down")},
	)
	rec := postResults(handler, validResultsRequest())
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestAnalysisResults_NoInstallation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &AnalysisResultsHandler{
		Logger:   logger,
		Jobs:     &fakeResultsJobs{},
		Projects: &fakeProjectsFull{fakeProjects: newFakeProjects()},
		Findings: &fakeFindingsWriter{},
	}
	b, _ := json.Marshal(validResultsRequest())
	req := httptest.NewRequest(http.MethodPost, "/v1/analysis/results", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}
