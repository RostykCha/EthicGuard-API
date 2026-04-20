package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/catalog"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

// DigestsHandler serves GET /v1/digests/latest — the most recent weekly
// snapshot for the caller's installation. Message text is rendered by the
// catalog at read time using the requested role (defaults to neutral).
type DigestsHandler struct {
	Logger  *slog.Logger
	Digests *store.Digests
	Catalog *catalog.Catalog
}

type digestFindingResponse struct {
	ID           int64             `json:"id"`
	IssueKey     string            `json:"issueKey"`
	Category     string            `json:"category"`
	Severity     string            `json:"severity"`
	Score        int               `json:"score"`
	MessageKey   string            `json:"messageKey"`
	Params       map[string]string `json:"params,omitempty"`
	Message      string            `json:"message"`
	RationaleTag string            `json:"rationaleTag,omitempty"`
}

type digestResponse struct {
	ID          int64                   `json:"id"`
	PeriodStart string                  `json:"periodStart"`
	PeriodEnd   string                  `json:"periodEnd"`
	Findings    []digestFindingResponse `json:"findings"`
}

func (h *DigestsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	inst := auth.InstallationFromContext(r.Context())
	if inst == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no installation"})
		return
	}
	d, err := h.Digests.GetLatest(r.Context(), inst.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Empty state: no digest generated yet for this install.
			writeJSON(w, http.StatusOK, map[string]any{"empty": true})
			return
		}
		h.Logger.Error("digests get latest failed", "err", err, "cloud_id", inst.CloudID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup failed"})
		return
	}
	rows, err := h.Digests.ResolveFindingsForDigest(r.Context(), inst.ID, d.FindingIDs)
	if err != nil {
		h.Logger.Error("digests resolve findings failed", "err", err, "digest_id", d.ID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resolve failed"})
		return
	}
	role := parseRole(r.URL.Query().Get("role"))
	resp := digestResponse{
		ID:          d.ID,
		PeriodStart: d.PeriodStart.UTC().Format("2006-01-02T15:04:05.000Z"),
		PeriodEnd:   d.PeriodEnd.UTC().Format("2006-01-02T15:04:05.000Z"),
		Findings:    make([]digestFindingResponse, 0, len(rows)),
	}
	for _, df := range rows {
		msg, err := h.Catalog.Resolve(df.Finding.MessageKey, df.Finding.Params, role)
		if err != nil {
			h.Logger.Error("catalog resolve failed",
				"err", err, "digest_id", d.ID, "message_key", df.Finding.MessageKey)
			msg = df.Finding.MessageKey
		}
		resp.Findings = append(resp.Findings, digestFindingResponse{
			ID:           df.Finding.ID,
			IssueKey:     df.IssueKey,
			Category:     df.Finding.Category,
			Severity:     df.Finding.Severity,
			Score:        df.Finding.Score,
			MessageKey:   df.Finding.MessageKey,
			Params:       df.Finding.Params,
			Message:      msg,
			RationaleTag: df.Finding.RationaleTag,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}
