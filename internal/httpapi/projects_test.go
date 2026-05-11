package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

type fakeProjects struct {
	getCalls int
	setCalls int
	stored   map[string]*store.ProjectConfig
	getErr   error
	setErr   error
	notFound bool
}

func newFakeProjects() *fakeProjects {
	return &fakeProjects{stored: map[string]*store.ProjectConfig{}}
}

func (f *fakeProjects) GetConfig(_ context.Context, _ int64, projectKey string) (*store.ProjectConfig, error) {
	f.getCalls++
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.notFound {
		return nil, store.ErrNotFound
	}
	cfg, ok := f.stored[projectKey]
	if !ok {
		return nil, store.ErrNotFound
	}
	return cfg, nil
}

func (f *fakeProjects) SetConfig(_ context.Context, _ int64, projectKey string, in store.ProjectConfig) (*store.ProjectConfig, error) {
	f.setCalls++
	if f.setErr != nil {
		return nil, f.setErr
	}
	cfg := &store.ProjectConfig{
		ProjectKey:             projectKey,
		TestedIssueTypes:       in.TestedIssueTypes,
		AgentEnabled:           in.AgentEnabled,
		AgentSeverityThreshold: in.AgentSeverityThreshold,
		AgentPromptAddendum:    in.AgentPromptAddendum,
	}
	f.stored[projectKey] = cfg
	return cfg, nil
}

type fakeAudits struct {
	calls []auditCall
}

type auditCall struct {
	installationID int64
	actor          string
	action         string
	target         string
	meta           map[string]any
}

func (f *fakeAudits) Log(_ context.Context, installationID int64, actor, action, target string, meta map[string]any) error {
	f.calls = append(f.calls, auditCall{installationID, actor, action, target, meta})
	return nil
}

// newTestHandler wires the handler behind the same path routing the real
// router uses, with a test installation pre-injected into the request
// context (mirroring what auth.Middleware would do for an authed request).
func newTestHandler(p ProjectsRepo, a AuditsRepo) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &ProjectsHandler{Logger: logger, Projects: p, Audits: a}
	mux := http.NewServeMux()
	mux.Handle("GET /v1/projects/{projectKey}/config", h)
	mux.Handle("PUT /v1/projects/{projectKey}/config", h)
	// Inject a fixed installation into context for every test request.
	inst := &store.Installation{ID: 42, CloudID: "cloud-xyz"}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(auth.WithInstallationForTest(r.Context(), inst))
		mux.ServeHTTP(w, r)
	})
}

func TestProjectsHandler_Get_DefaultsWhenMissing(t *testing.T) {
	p := newFakeProjects()
	handler := newTestHandler(p, &fakeAudits{})

	req := httptest.NewRequest(http.MethodGet, "/v1/projects/PROJ/config", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var got projectConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ProjectKey != "PROJ" {
		t.Errorf("projectKey = %q, want PROJ", got.ProjectKey)
	}
	if len(got.TestedIssueTypes) != 0 {
		t.Errorf("testedIssueTypes = %v, want empty", got.TestedIssueTypes)
	}
	// Defaults: agent enabled, medium threshold, empty addendum.
	if !got.AgentEnabled {
		t.Errorf("agentEnabled = false, want true (default)")
	}
	if got.AgentSeverityThreshold != "medium" {
		t.Errorf("agentSeverityThreshold = %q, want medium", got.AgentSeverityThreshold)
	}
	if got.AgentPromptAddendum != "" {
		t.Errorf("agentPromptAddendum = %q, want empty", got.AgentPromptAddendum)
	}
}

func TestProjectsHandler_Get_ReturnsStored(t *testing.T) {
	p := newFakeProjects()
	p.stored["PROJ"] = &store.ProjectConfig{
		ProjectKey:             "PROJ",
		TestedIssueTypes:       []string{"10001", "10002"},
		AgentEnabled:           false,
		AgentSeverityThreshold: "high",
		AgentPromptAddendum:    "focus on accessibility",
	}
	handler := newTestHandler(p, &fakeAudits{})

	req := httptest.NewRequest(http.MethodGet, "/v1/projects/PROJ/config", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got projectConfigResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.TestedIssueTypes) != 2 || got.TestedIssueTypes[0] != "10001" {
		t.Errorf("testedIssueTypes = %v, want [10001 10002]", got.TestedIssueTypes)
	}
	if got.AgentEnabled {
		t.Errorf("agentEnabled = true, want false")
	}
	if got.AgentSeverityThreshold != "high" {
		t.Errorf("agentSeverityThreshold = %q, want high", got.AgentSeverityThreshold)
	}
	if got.AgentPromptAddendum != "focus on accessibility" {
		t.Errorf("agentPromptAddendum = %q", got.AgentPromptAddendum)
	}
}

func TestProjectsHandler_Put_SanitizesAndPersists(t *testing.T) {
	p := newFakeProjects()
	a := &fakeAudits{}
	handler := newTestHandler(p, a)

	enabled := false
	body, _ := json.Marshal(putConfigRequest{
		TestedIssueTypes:       []string{" 10001 ", "10001", "", "10002"},
		AgentEnabled:           &enabled,
		AgentSeverityThreshold: "high",
		AgentPromptAddendum:    "   prioritise accessibility   ",
		ActorAccountID:         "acct-1",
	})
	req := httptest.NewRequest(http.MethodPut, "/v1/projects/PROJ/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := p.stored["PROJ"]
	if got == nil || len(got.TestedIssueTypes) != 2 {
		t.Fatalf("stored = %+v, want trimmed/deduped to 2 entries", got)
	}
	if got.TestedIssueTypes[0] != "10001" || got.TestedIssueTypes[1] != "10002" {
		t.Errorf("stored types = %v, want [10001 10002]", got.TestedIssueTypes)
	}
	if got.AgentEnabled {
		t.Errorf("agentEnabled = true, want false")
	}
	if got.AgentSeverityThreshold != "high" {
		t.Errorf("agentSeverityThreshold = %q, want high", got.AgentSeverityThreshold)
	}
	if got.AgentPromptAddendum != "prioritise accessibility" {
		t.Errorf("agentPromptAddendum = %q, want trimmed value", got.AgentPromptAddendum)
	}
	if len(a.calls) != 1 {
		t.Fatalf("audit calls = %d, want 1", len(a.calls))
	}
	if a.calls[0].action != auditActionConfigUpdated || a.calls[0].actor != "acct-1" {
		t.Errorf("audit call = %+v", a.calls[0])
	}
}

func TestProjectsHandler_Put_PreservesUnsetFields(t *testing.T) {
	// A PUT that doesn't include agentEnabled / agentSeverityThreshold should
	// keep the previously stored values, not reset them to defaults.
	p := newFakeProjects()
	p.stored["PROJ"] = &store.ProjectConfig{
		ProjectKey:             "PROJ",
		TestedIssueTypes:       []string{"old"},
		AgentEnabled:           false,
		AgentSeverityThreshold: "high",
		AgentPromptAddendum:    "keep me",
	}
	handler := newTestHandler(p, &fakeAudits{})

	body, _ := json.Marshal(putConfigRequest{
		TestedIssueTypes: []string{"10001"},
	})
	req := httptest.NewRequest(http.MethodPut, "/v1/projects/PROJ/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got := p.stored["PROJ"]
	if got.AgentEnabled {
		t.Errorf("agentEnabled was overwritten to true; expected preserved false")
	}
	if got.AgentSeverityThreshold != "high" {
		t.Errorf("threshold overwritten to %q; expected preserved high", got.AgentSeverityThreshold)
	}
	// Empty addendum in the request still clears the stored value — by design,
	// the UI always sends the full current value, so empty means "cleared".
	if got.AgentPromptAddendum != "" {
		t.Errorf("addendum = %q, expected explicit clear to empty", got.AgentPromptAddendum)
	}
}

func TestProjectsHandler_Put_RejectsInvalidSeverity(t *testing.T) {
	p := newFakeProjects()
	handler := newTestHandler(p, &fakeAudits{})

	body, _ := json.Marshal(putConfigRequest{
		TestedIssueTypes:       []string{"10001"},
		AgentSeverityThreshold: "critical", // not a valid enum
	})
	req := httptest.NewRequest(http.MethodPut, "/v1/projects/PROJ/config", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if p.setCalls != 0 {
		t.Errorf("set calls = %d, want 0 (rejected before persist)", p.setCalls)
	}
}

func TestProjectsHandler_Put_RejectsAddendumTooLong(t *testing.T) {
	p := newFakeProjects()
	handler := newTestHandler(p, &fakeAudits{})

	body, _ := json.Marshal(putConfigRequest{
		TestedIssueTypes:    []string{"10001"},
		AgentPromptAddendum: strings.Repeat("x", maxPromptAddendumBytes+1),
	})
	req := httptest.NewRequest(http.MethodPut, "/v1/projects/PROJ/config", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if p.setCalls != 0 {
		t.Errorf("set calls = %d, want 0 (rejected before persist)", p.setCalls)
	}
}

func TestProjectsHandler_Put_RejectsTooManyTypes(t *testing.T) {
	p := newFakeProjects()
	handler := newTestHandler(p, &fakeAudits{})

	types := make([]string, maxIssueTypes+1)
	for i := range types {
		types[i] = "id"
	}
	body, _ := json.Marshal(putConfigRequest{TestedIssueTypes: types})
	req := httptest.NewRequest(http.MethodPut, "/v1/projects/PROJ/config", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if p.setCalls != 0 {
		t.Errorf("set calls = %d, want 0 (rejected before persist)", p.setCalls)
	}
}

func TestProjectsHandler_MethodNotAllowed(t *testing.T) {
	handler := newTestHandler(newFakeProjects(), &fakeAudits{})

	req := httptest.NewRequest(http.MethodDelete, "/v1/projects/PROJ/config", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// The router itself rejects unregistered method+path pairs with 405.
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 405 or 404", rec.Code)
	}
}
