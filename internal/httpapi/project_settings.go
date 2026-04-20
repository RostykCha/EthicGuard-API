package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

// ProjectSettingsHandler serves GET/PUT /v1/projects/{projectKey}/settings.
// Tenant-isolated by the caller's installation — projects are scoped per
// installation in the schema.
type ProjectSettingsHandler struct {
	Logger   *slog.Logger
	Projects *store.Projects
	Audit    *store.Audit
}

type projectSettingsResponse struct {
	ProjectKey          string         `json:"projectKey"`
	ConfidenceThreshold int            `json:"confidenceThreshold"`
	ThresholdOverrides  map[string]int `json:"thresholdOverrides"`
	TestedIssueTypes    []string       `json:"testedIssueTypes"`
}

// projectSettingsUpdate accepts partial updates. Fields are pointers so the
// caller can update one dimension (threshold OR tested issue types) without
// clobbering the other.
type projectSettingsUpdate struct {
	ConfidenceThreshold *int      `json:"confidenceThreshold,omitempty"`
	TestedIssueTypes    *[]string `json:"testedIssueTypes,omitempty"`
}

func (h *ProjectSettingsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	inst := auth.InstallationFromContext(r.Context())
	if inst == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no installation"})
		return
	}
	projectKey := r.PathValue("projectKey")
	if projectKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing projectKey"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.serveGet(w, r, inst.ID, projectKey)
	case http.MethodPut:
		h.servePut(w, r, inst, projectKey)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *ProjectSettingsHandler) serveGet(w http.ResponseWriter, r *http.Request, installationID int64, projectKey string) {
	p, err := h.Projects.GetByKey(r.Context(), installationID, projectKey)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Return defaults for projects that haven't been seen yet so the
			// UI can render a slider at 0 without a pre-flight request.
			writeJSON(w, http.StatusOK, projectSettingsResponse{
				ProjectKey:          projectKey,
				ConfidenceThreshold: 0,
				ThresholdOverrides:  map[string]int{},
				TestedIssueTypes:    []string{},
			})
			return
		}
		h.Logger.Error("projects get failed", "err", err, "project_key", projectKey)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup failed"})
		return
	}
	writeJSON(w, http.StatusOK, projectSettingsResponse{
		ProjectKey:          p.ProjectKey,
		ConfidenceThreshold: p.ConfidenceThreshold,
		ThresholdOverrides:  p.ThresholdOverrides,
		TestedIssueTypes:    nonNilStrings(p.TestedIssueTypes),
	})
}

func (h *ProjectSettingsHandler) servePut(w http.ResponseWriter, r *http.Request, inst *store.Installation, projectKey string) {
	var req projectSettingsUpdate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.ConfidenceThreshold == nil && req.TestedIssueTypes == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no fields to update"})
		return
	}

	if req.ConfidenceThreshold != nil {
		v := *req.ConfidenceThreshold
		if v < 0 || v > 100 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "confidenceThreshold must be 0-100"})
			return
		}
		if err := h.Projects.UpdateThreshold(r.Context(), inst.ID, projectKey, v); err != nil {
			h.Logger.Error("update threshold failed", "err", err, "project_key", projectKey)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "persist failed"})
			return
		}
		if h.Audit != nil {
			if err := h.Audit.Log(r.Context(), inst.ID, "", "project.threshold_updated", projectKey, map[string]any{
				"confidence_threshold": v,
			}); err != nil {
				h.Logger.Warn("audit log failed", "err", err)
			}
		}
	}

	if req.TestedIssueTypes != nil {
		ids := sanitizeIssueTypeIDs(*req.TestedIssueTypes)
		if err := h.Projects.SetTestedIssueTypes(r.Context(), inst.ID, projectKey, ids); err != nil {
			h.Logger.Error("update tested issue types failed", "err", err, "project_key", projectKey)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "persist failed"})
			return
		}
		if h.Audit != nil {
			if err := h.Audit.Log(r.Context(), inst.ID, "", "project.tested_issue_types_updated", projectKey, map[string]any{
				"count": len(ids),
			}); err != nil {
				h.Logger.Warn("audit log failed", "err", err)
			}
		}
	}

	p, err := h.Projects.GetByKey(r.Context(), inst.ID, projectKey)
	if err != nil {
		h.Logger.Error("reload after update failed", "err", err, "project_key", projectKey)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload failed"})
		return
	}
	writeJSON(w, http.StatusOK, projectSettingsResponse{
		ProjectKey:          p.ProjectKey,
		ConfidenceThreshold: p.ConfidenceThreshold,
		ThresholdOverrides:  p.ThresholdOverrides,
		TestedIssueTypes:    nonNilStrings(p.TestedIssueTypes),
	})
}

// sanitizeIssueTypeIDs filters incoming ids to Jira's numeric-id shape so a
// client can't smuggle issue content into the column. Jira issue-type ids
// are decimal strings (e.g. "10001"); anything else is dropped. Dedupes.
func sanitizeIssueTypeIDs(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, s := range in {
		if s == "" || len(s) > 32 {
			continue
		}
		ok := true
		for _, c := range s {
			if c < '0' || c > '9' {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
