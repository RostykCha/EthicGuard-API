package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

// PreferencesHandler serves GET/PUT /v1/preferences — per-user persona
// override (Phase 3 #10). Tenant-isolated by the caller's installation.
//
// The JWT doesn't currently carry the Jira account id; rather than extend
// claims (which requires synchronised changes on the UI side), we accept
// the account id as a query param on GET and a body field on PUT. This is
// safe because the JWT already binds the cloudId, so one installation
// cannot read another's preferences.
type PreferencesHandler struct {
	Logger          *slog.Logger
	UserPreferences *store.UserPreferences
	Audit           *store.Audit
}

type preferenceResponse struct {
	AccountID string `json:"accountId"`
	Persona   string `json:"persona,omitempty"`
}

type preferenceUpdate struct {
	AccountID string `json:"accountId"`
	Persona   string `json:"persona"` // "" | "pm" | "qa" | "dev"
}

var allowedPersonas = map[string]bool{
	"":    true,
	"pm":  true,
	"qa":  true,
	"dev": true,
}

func (h *PreferencesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	inst := auth.InstallationFromContext(r.Context())
	if inst == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no installation"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.serveGet(w, r, inst.ID)
	case http.MethodPut:
		h.servePut(w, r, inst)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *PreferencesHandler) serveGet(w http.ResponseWriter, r *http.Request, installationID int64) {
	accountID := r.URL.Query().Get("accountId")
	if accountID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "accountId required"})
		return
	}
	pref, err := h.UserPreferences.Get(r.Context(), installationID, accountID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, preferenceResponse{AccountID: accountID})
			return
		}
		h.Logger.Error("preferences get failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup failed"})
		return
	}
	writeJSON(w, http.StatusOK, preferenceResponse{
		AccountID: pref.AccountID,
		Persona:   pref.Persona,
	})
}

func (h *PreferencesHandler) servePut(w http.ResponseWriter, r *http.Request, inst *store.Installation) {
	var req preferenceUpdate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.AccountID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "accountId required"})
		return
	}
	if !allowedPersonas[req.Persona] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid persona"})
		return
	}
	pref, err := h.UserPreferences.Upsert(r.Context(), inst.ID, req.AccountID, req.Persona)
	if err != nil {
		h.Logger.Error("preferences upsert failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "persist failed"})
		return
	}
	if h.Audit != nil {
		if err := h.Audit.Log(r.Context(), inst.ID, req.AccountID, "preferences.updated", req.AccountID, map[string]any{
			"persona": req.Persona,
		}); err != nil {
			h.Logger.Warn("audit log failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, preferenceResponse{
		AccountID: pref.AccountID,
		Persona:   pref.Persona,
	})
}
