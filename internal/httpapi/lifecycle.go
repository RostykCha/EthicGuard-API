package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

// InstallationsRepo is the lifecycle webhook's view of the installations
// table — narrowed to the two operations it performs. Declared here so tests
// can substitute a fake; production wiring passes *store.Installations.
type InstallationsRepo interface {
	Upsert(ctx context.Context, cloudID, sharedSecret string) (*store.Installation, error)
	DeleteByCloudID(ctx context.Context, cloudID string) error
}

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
	Installations   InstallationsRepo
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
		unauthorized(w, "unauthorized")
		return
	}

	var req lifecycleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid json")
		return
	}
	if req.CloudID == "" || req.CloudID != claims.CloudID {
		badRequest(w, "cloudId mismatch")
		return
	}

	switch req.Event {
	case "install":
		if len(req.SharedSecret) < 32 {
			badRequest(w, "sharedSecret too short")
			return
		}
		if _, err := h.Installations.Upsert(r.Context(), req.CloudID, req.SharedSecret); err != nil {
			internalErr(w, h.Logger, err, "persist failed", "cloud_id", req.CloudID)
			return
		}
		h.Logger.Info("installation registered", "cloud_id", req.CloudID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "installed"})

	case "uninstall":
		if err := h.Installations.DeleteByCloudID(r.Context(), req.CloudID); err != nil {
			if store.IsNotFound(err) {
				// Idempotent: treat as success so Forge doesn't retry forever.
				writeJSON(w, http.StatusOK, map[string]string{"status": "not-installed"})
				return
			}
			internalErr(w, h.Logger, err, "delete failed", "cloud_id", req.CloudID)
			return
		}
		h.Logger.Info("installation removed", "cloud_id", req.CloudID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "uninstalled"})

	default:
		badRequest(w, "unknown event")
	}
}
