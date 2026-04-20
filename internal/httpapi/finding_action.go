package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

// FindingActionHandler serves POST /v1/findings/{id}/action. Records the
// caller's accept/dismiss response on a finding. Tenant-isolated: the finding
// must belong to a job in the caller's installation.
//
// Zero-retention: `action` and `reason` are enum strings; no free text is
// ever accepted. The request body's `reason` field is validated against a
// whitelist and rejected otherwise.
type FindingActionHandler struct {
	Logger   *slog.Logger
	Findings *store.Findings
	Actions  *store.FindingActions
	Audit    *store.Audit
}

type findingActionRequest struct {
	Action         string `json:"action"`
	Reason         string `json:"reason,omitempty"`
	ActorAccountID string `json:"actorAccountId,omitempty"`
}

type findingActionResponse struct {
	ID        int64  `json:"id"`
	FindingID int64  `json:"findingId"`
	Action    string `json:"action"`
	Reason    string `json:"reason,omitempty"`
	CreatedAt string `json:"createdAt"`
}

var allowedActions = map[string]bool{
	"accept":  true,
	"dismiss": true,
}

var allowedDismissReasons = map[string]bool{
	"false_positive": true,
	"wont_fix":       true,
	"duplicate":      true,
	"noise":          true,
	"other":          true,
}

func (h *FindingActionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	inst := auth.InstallationFromContext(r.Context())
	if inst == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no installation"})
		return
	}

	findingID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || findingID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid finding id"})
		return
	}

	var req findingActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if !allowedActions[req.Action] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid action"})
		return
	}
	if req.Action == "dismiss" && req.Reason != "" && !allowedDismissReasons[req.Reason] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid reason"})
		return
	}
	if req.Action == "accept" {
		// Accept has no reason — discard any the caller might have sent.
		req.Reason = ""
	}

	// Tenant isolation: verify the finding's job belongs to this installation.
	if _, err := h.Findings.GetByIDForInstallation(r.Context(), findingID, inst.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		h.Logger.Error("finding lookup failed", "err", err, "finding_id", findingID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup failed"})
		return
	}

	fa, err := h.Actions.Upsert(r.Context(), findingID, req.Action, req.Reason, req.ActorAccountID)
	if err != nil {
		h.Logger.Error("finding action upsert failed", "err", err, "finding_id", findingID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "persist failed"})
		return
	}

	if h.Audit != nil {
		meta := map[string]any{"finding_id": findingID, "action": req.Action}
		if req.Reason != "" {
			meta["reason"] = req.Reason
		}
		if err := h.Audit.Log(r.Context(), inst.ID, req.ActorAccountID, "finding.action", strconv.FormatInt(findingID, 10), meta); err != nil {
			h.Logger.Warn("audit log failed", "err", err)
		}
	}

	writeJSON(w, http.StatusOK, findingActionResponse{
		ID:        fa.ID,
		FindingID: fa.FindingID,
		Action:    fa.Action,
		Reason:    fa.Reason,
		CreatedAt: fa.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	})
}
