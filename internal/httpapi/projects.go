package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

// ProjectsRepo is the projects-storage surface this handler needs. Declared as
// an interface so handler tests can fake it without a real Postgres.
type ProjectsRepo interface {
	GetConfig(ctx context.Context, installationID int64, projectKey string) (*store.ProjectConfig, error)
	SetConfig(ctx context.Context, installationID int64, projectKey string, in store.ProjectConfig) (*store.ProjectConfig, error)
}

// AuditsRepo is the audit-log surface this handler needs.
type AuditsRepo interface {
	Log(ctx context.Context, installationID int64, actorAccountID, action, target string, meta map[string]any) error
}

// ProjectsHandler serves /v1/projects/{projectKey}/config — admin reads and
// writes the per-project EthicGuard configuration. Authenticated by the same
// Forge-JWT middleware as /v1/analysis; the installation comes from context.
type ProjectsHandler struct {
	Logger   *slog.Logger
	Projects ProjectsRepo
	Audits   AuditsRepo
}

type projectConfigResponse struct {
	ProjectKey             string   `json:"projectKey"`
	TestedIssueTypes       []string `json:"testedIssueTypes"`
	AgentEnabled           bool     `json:"agentEnabled"`
	AgentSeverityThreshold string   `json:"agentSeverityThreshold"`
	AgentPromptAddendum    string   `json:"agentPromptAddendum"`
}

type putConfigRequest struct {
	TestedIssueTypes       []string `json:"testedIssueTypes"`
	AgentEnabled           *bool    `json:"agentEnabled,omitempty"`
	AgentSeverityThreshold string   `json:"agentSeverityThreshold,omitempty"`
	AgentPromptAddendum    string   `json:"agentPromptAddendum,omitempty"`
	ActorAccountID         string   `json:"actorAccountId,omitempty"`
}

// auditActionConfigUpdated is the audit-log action token for a successful
// PUT /v1/projects/{key}/config. Stable string — UI/audit dashboards key
// off it.
const auditActionConfigUpdated = "project.config.updated"

// Validation constants and isValidSeverity live in validation.go.

// defaultConfig is what an installation gets before the admin saves anything.
// agent_enabled=true matches the migration default — a brand-new project that
// has issue types in scope analyses without further opt-in.
func defaultConfig(projectKey string) *store.ProjectConfig {
	return &store.ProjectConfig{
		ProjectKey:             projectKey,
		TestedIssueTypes:       []string{},
		AgentEnabled:           true,
		AgentSeverityThreshold: defaultSeverityThreshold,
		AgentPromptAddendum:    "",
	}
}

// ServeHTTP dispatches GET/PUT under the path /v1/projects/{projectKey}/config.
// Other methods return 405.
func (h *ProjectsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	inst := auth.InstallationFromContext(r.Context())
	if inst == nil {
		unauthorized(w, "no installation")
		return
	}
	projectKey := strings.TrimSpace(r.PathValue("projectKey"))
	if projectKey == "" {
		badRequest(w, "projectKey required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r, inst, projectKey)
	case http.MethodPut:
		h.handlePut(w, r, inst, projectKey)
	default:
		methodNotAllowed(w, "GET, PUT", "method not allowed")
	}
}

func (h *ProjectsHandler) handleGet(w http.ResponseWriter, r *http.Request, inst *store.Installation, projectKey string) {
	cfg, err := h.Projects.GetConfig(r.Context(), inst.ID, projectKey)
	if err != nil {
		if store.IsNotFound(err) {
			writeJSON(w, http.StatusOK, configToResponse(defaultConfig(projectKey)))
			return
		}
		internalErr(w, h.Logger, err, "config lookup failed",
			"cloud_id", inst.CloudID, "project_key", projectKey)
		return
	}
	writeJSON(w, http.StatusOK, configToResponse(cfg))
}

func (h *ProjectsHandler) handlePut(w http.ResponseWriter, r *http.Request, inst *store.Installation, projectKey string) {
	var req putConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid json")
		return
	}
	types, errMsg := sanitizeIssueTypes(req.TestedIssueTypes)
	if errMsg != "" {
		badRequest(w, errMsg)
		return
	}

	// Load existing config so an admin who only updates one field doesn't
	// have to round-trip everything — falls back to defaults if no row yet.
	existing, err := h.Projects.GetConfig(r.Context(), inst.ID, projectKey)
	if err != nil {
		if !store.IsNotFound(err) {
			internalErr(w, h.Logger, err, "config lookup failed",
				"cloud_id", inst.CloudID, "project_key", projectKey)
			return
		}
		existing = defaultConfig(projectKey)
	}

	merged := store.ProjectConfig{
		ProjectKey:             projectKey,
		TestedIssueTypes:       types,
		AgentEnabled:           existing.AgentEnabled,
		AgentSeverityThreshold: existing.AgentSeverityThreshold,
		AgentPromptAddendum:    existing.AgentPromptAddendum,
	}
	if req.AgentEnabled != nil {
		merged.AgentEnabled = *req.AgentEnabled
	}
	if req.AgentSeverityThreshold != "" {
		if !isValidSeverity(req.AgentSeverityThreshold) {
			badRequest(w, "invalid agentSeverityThreshold")
			return
		}
		merged.AgentSeverityThreshold = req.AgentSeverityThreshold
	}
	if len(req.AgentPromptAddendum) > maxPromptAddendumBytes {
		badRequest(w, "agentPromptAddendum too long")
		return
	}
	// Empty string is a valid explicit clear; we always overwrite the addendum
	// to whatever the client sent (the UI always sends the full current value).
	merged.AgentPromptAddendum = strings.TrimSpace(req.AgentPromptAddendum)

	cfg, err := h.Projects.SetConfig(r.Context(), inst.ID, projectKey, merged)
	if err != nil {
		internalErr(w, h.Logger, err, "config update failed",
			"cloud_id", inst.CloudID, "project_key", projectKey)
		return
	}

	if h.Audits != nil {
		meta := map[string]any{
			"project_key":               projectKey,
			"tested_issue_types":        types,
			"agent_enabled":             merged.AgentEnabled,
			"agent_severity_threshold":  merged.AgentSeverityThreshold,
			"agent_prompt_addendum_len": len(merged.AgentPromptAddendum),
		}
		if err := h.Audits.Log(r.Context(), inst.ID, req.ActorAccountID, auditActionConfigUpdated, "project:"+projectKey, meta); err != nil {
			h.Logger.Warn("audit log failed",
				"err", err, "cloud_id", inst.CloudID, "project_key", projectKey)
		}
	}

	h.Logger.Info("project config updated",
		"cloud_id", inst.CloudID,
		"project_key", projectKey,
		"tested_issue_types_count", len(types),
		"agent_enabled", merged.AgentEnabled,
		"agent_severity_threshold", merged.AgentSeverityThreshold,
		"actor", req.ActorAccountID,
	)
	writeJSON(w, http.StatusOK, configToResponse(cfg))
}

func configToResponse(cfg *store.ProjectConfig) projectConfigResponse {
	types := cfg.TestedIssueTypes
	if types == nil {
		types = []string{}
	}
	return projectConfigResponse{
		ProjectKey:             cfg.ProjectKey,
		TestedIssueTypes:       types,
		AgentEnabled:           cfg.AgentEnabled,
		AgentSeverityThreshold: cfg.AgentSeverityThreshold,
		AgentPromptAddendum:    cfg.AgentPromptAddendum,
	}
}

// sanitizeIssueTypes trims, drops empties, dedupes, and bounds the list. The
// API treats issue type IDs as opaque strings — Jira's REST returns them as
// numeric-looking strings but we don't depend on that shape.
func sanitizeIssueTypes(in []string) (out []string, errMsg string) {
	if in == nil {
		return []string{}, ""
	}
	if len(in) > maxIssueTypes {
		return nil, "too many issue types"
	}
	seen := make(map[string]struct{}, len(in))
	out = make([]string, 0, len(in))
	for _, raw := range in {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		if len(t) > maxIssueTypeIDLen {
			return nil, "issue type id too long"
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out, ""
}
