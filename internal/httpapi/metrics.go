// Package httpapi handler for per-project metrics. See router.go for the
// package-level invariants this file follows.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ethicguard/ethicguard-api/internal/auth"
)

// MetricsRepo is the aggregate-query surface this handler needs. Declared
// as an interface so handler tests fake it without a real Postgres.
// *store.Jobs is the production implementation.
type MetricsRepo interface {
	CountCoveredIssues(ctx context.Context, installationID int64, projectKey string) (int, error)
}

// MetricsHandler serves GET /v1/projects/{projectKey}/metrics — the
// admin-facing aggregate view rendered by the settings page KPI card.
// Authenticated by the same Forge-JWT middleware as the rest of /v1;
// the installation comes from context.
type MetricsHandler struct {
	Logger  *slog.Logger
	Metrics MetricsRepo
}

type projectMetricsResponse struct {
	ProjectKey        string `json:"projectKey"`
	CoveredIssueCount int    `json:"coveredIssueCount"`
}

func (h *MetricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	inst := auth.InstallationFromContext(r.Context())
	if inst == nil {
		unauthorized(w, "no installation")
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET", "method not allowed")
		return
	}
	projectKey := strings.TrimSpace(r.PathValue("projectKey"))
	if projectKey == "" {
		badRequest(w, "projectKey required")
		return
	}

	count, err := h.Metrics.CountCoveredIssues(r.Context(), inst.ID, projectKey)
	if err != nil {
		internalErr(w, h.Logger, err, "metrics lookup failed",
			"cloud_id", inst.CloudID, "project_key", projectKey)
		return
	}
	writeJSON(w, http.StatusOK, projectMetricsResponse{
		ProjectKey:        projectKey,
		CoveredIssueCount: count,
	})
}
