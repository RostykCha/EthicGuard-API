package httpapi

import (
	"context"
	"encoding/json"
	"errors"
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
	SetTestedIssueTypes(ctx context.Context, installationID int64, projectKey string, types []string) (*store.ProjectConfig, error)
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
	ProjectKey       string   `json:"projectKey"`
	TestedIssueTypes []string `json:"testedIssueTypes"`
}

type putConfigRequest struct {
	TestedIssueTypes []string `json:"testedIssueTypes"`
	ActorAccountID   string   `json:"actorAccountId,omitempty"`
}

const (
	auditActionConfigUpdated = "project.config.updated"
	maxIssueTypes            = 50
	maxIssueTypeIDLen        = 64
)

// ServeHTTP dispatches GET/PUT under the path /v1/projects/{projectKey}/config.
// Other methods return 405.
func (h *ProjectsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	inst := auth.InstallationFromContext(r.Context())
	if inst == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no installation"})
		return
	}
	projectKey := strings.TrimSpace(r.PathValue("projectKey"))
	if projectKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "projectKey required"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r, inst, projectKey)
	case http.MethodPut:
		h.handlePut(w, r, inst, projectKey)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *ProjectsHandler) handleGet(w http.ResponseWriter, r *http.Request, inst *store.Installation, projectKey string) {
	cfg, err := h.Projects.GetConfig(r.Context(), inst.ID, projectKey)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, projectConfigResponse{
				ProjectKey:       projectKey,
				TestedIssueTypes: []string{},
			})
			return
		}
		h.Logger.Error("projects get config failed",
			"err", err, "cloud_id", inst.CloudID, "project_key", projectKey)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config lookup failed"})
		return
	}
	writeJSON(w, http.StatusOK, projectConfigResponse{
		ProjectKey:       cfg.ProjectKey,
		TestedIssueTypes: cfg.TestedIssueTypes,
	})
}

func (h *ProjectsHandler) handlePut(w http.ResponseWriter, r *http.Request, inst *store.Installation, projectKey string) {
	var req putConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	types, errMsg := sanitizeIssueTypes(req.TestedIssueTypes)
	if errMsg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errMsg})
		return
	}

	cfg, err := h.Projects.SetTestedIssueTypes(r.Context(), inst.ID, projectKey, types)
	if err != nil {
		h.Logger.Error("projects set tested types failed",
			"err", err, "cloud_id", inst.CloudID, "project_key", projectKey)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config update failed"})
		return
	}

	if h.Audits != nil {
		meta := map[string]any{
			"project_key":        projectKey,
			"tested_issue_types": types,
		}
		if err := h.Audits.Log(r.Context(), inst.ID, req.ActorAccountID, auditActionConfigUpdated, "project:"+projectKey, meta); err != nil {
			// Audit failure should not fail the user's write — log and continue.
			h.Logger.Warn("audit log failed",
				"err", err, "cloud_id", inst.CloudID, "project_key", projectKey)
		}
	}

	h.Logger.Info("project config updated",
		"cloud_id", inst.CloudID,
		"project_key", projectKey,
		"tested_issue_types_count", len(types),
		"actor", req.ActorAccountID,
	)
	writeJSON(w, http.StatusOK, projectConfigResponse{
		ProjectKey:       cfg.ProjectKey,
		TestedIssueTypes: cfg.TestedIssueTypes,
	})
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
