package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

// LifecycleHandler serves POST /v1/installations/lifecycle — the webhook the
// Forge app calls on install and uninstall events. Authentication is a
// bearer JWT signed with ETHICGUARD_INSTALLER_SECRET (pre-shared, bootstraps
// before any per-install secret exists). Payload:
//
//	{ "event": "install" | "uninstall",
//	  "cloudId": "...",
//	  "sharedSecret": "hex32..."   // required for install
//	}
type LifecycleHandler struct {
	Logger          *slog.Logger
	Installations   *store.Installations
	InstallerSecret string
}

type lifecycleReq struct {
	Event        string `json:"event"`
	CloudID      string `json:"cloudId"`
	SharedSecret string `json:"sharedSecret,omitempty"`
}

func (h *LifecycleHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := ""
	if v := r.Header.Get("Authorization"); len(v) > 7 && v[:7] == "Bearer " {
		token = v[7:]
	}
	claims, err := auth.Verify(token, h.InstallerSecret, auth.AudienceInstaller)
	if err != nil {
		h.Logger.Warn("lifecycle auth failed", "err", err)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req lifecycleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.CloudID == "" || req.CloudID != claims.CloudID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cloudId mismatch"})
		return
	}

	switch req.Event {
	case "install":
		if len(req.SharedSecret) < 32 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sharedSecret too short"})
			return
		}
		if _, err := h.Installations.Upsert(r.Context(), req.CloudID, req.SharedSecret); err != nil {
			h.Logger.Error("lifecycle upsert failed", "err", err, "cloud_id", req.CloudID)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "persist failed"})
			return
		}
		h.Logger.Info("installation registered", "cloud_id", req.CloudID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "installed"})

	case "uninstall":
		if err := h.Installations.DeleteByCloudID(r.Context(), req.CloudID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// Idempotent: treat as success so Forge doesn't retry forever.
				writeJSON(w, http.StatusOK, map[string]string{"status": "not-installed"})
				return
			}
			h.Logger.Error("lifecycle delete failed", "err", err, "cloud_id", req.CloudID)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete failed"})
			return
		}
		h.Logger.Info("installation removed", "cloud_id", req.CloudID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "uninstalled"})

	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown event"})
	}
}
