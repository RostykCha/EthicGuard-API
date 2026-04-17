package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/ethicguard/ethicguard-api/internal/analysis"
	"github.com/ethicguard/ethicguard-api/internal/auth"
)

// AnalysisHandler serves POST /v1/analysis — synchronous AC quality analysis.
// For MVP this runs the Claude call inline (no job queue). The normalized
// Jira content from the Forge UI is held only in memory for the duration of
// the request and is never written to Postgres.
type AnalysisHandler struct {
	Logger *slog.Logger
	LLM    analysis.LLM
}

func (h *AnalysisHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	inst := auth.InstallationFromContext(r.Context())
	if inst == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no installation"})
		return
	}

	var req analysis.AnalysisRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.IssueKey == "" || req.Payload.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "issueKey and payload.key are required"})
		return
	}

	start := time.Now()
	resp, err := analysis.Run(r.Context(), h.LLM, &req)
	duration := time.Since(start)

	if err != nil {
		h.Logger.Error("analysis failed",
			"err", err,
			"issue_key", req.IssueKey,
			"cloud_id", inst.CloudID,
			"duration_ms", duration.Milliseconds(),
		)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "analysis failed"})
		return
	}

	h.Logger.Info("analysis complete",
		"issue_key", req.IssueKey,
		"cloud_id", inst.CloudID,
		"findings", len(resp.Findings),
		"duration_ms", duration.Milliseconds(),
	)
	writeJSON(w, http.StatusOK, resp)
}
