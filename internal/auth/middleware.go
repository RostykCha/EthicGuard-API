package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ethicguard/ethicguard-api/internal/store"
)

type ctxKey int

const (
	ctxInstallation ctxKey = iota
)

// InstallationFromContext pulls the authenticated installation out of a
// request context. Only populated inside routes protected by Middleware.
func InstallationFromContext(ctx context.Context) *store.Installation {
	if inst, ok := ctx.Value(ctxInstallation).(*store.Installation); ok {
		return inst
	}
	return nil
}

// MiddlewareDeps is the minimal surface Middleware needs. Declaring it as an
// interface keeps the auth package free of a direct pgx dependency in tests.
type MiddlewareDeps interface {
	GetByCloudID(ctx context.Context, cloudID string) (*store.Installation, error)
}

// Middleware returns an http middleware that verifies the Authorization
// header Bearer JWT against the installation's per-cloudId shared secret.
// On success it injects the Installation into the request context. On any
// failure it writes 401 and stops.
func Middleware(logger *slog.Logger, installations MiddlewareDeps, audience string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			if token == "" {
				writeAuthError(w, "missing bearer token")
				return
			}
			// Parse token once *without* verification to extract the cloudId,
			// so we can look up the right shared secret. Then verify with it.
			cloudID, err := peekCloudID(token)
			if err != nil || cloudID == "" {
				writeAuthError(w, "unreadable token")
				return
			}
			inst, err := installations.GetByCloudID(r.Context(), cloudID)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					writeAuthError(w, "unknown installation")
					return
				}
				logger.Error("middleware installations lookup failed",
					"err", err, "cloud_id", cloudID)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			claims, err := Verify(token, inst.SharedSecret, audience)
			if err != nil {
				writeAuthError(w, "invalid signature")
				return
			}
			if claims.CloudID != inst.CloudID {
				writeAuthError(w, "cloudId mismatch")
				return
			}
			ctx := context.WithValue(r.Context(), ctxInstallation, inst)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// peekCloudID extracts the cloudId claim without verifying the signature.
// Used to route lookups before we know which shared secret to use.
func peekCloudID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("not a compact JWS")
	}
	payload, err := base64URLDecode(parts[1])
	if err != nil {
		return "", err
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return "", err
	}
	if v, ok := m["cloudId"].(string); ok {
		return v, nil
	}
	return "", errors.New("no cloudId claim")
}

func base64URLDecode(s string) ([]byte, error) {
	pad := 4 - (len(s) % 4)
	if pad != 4 {
		s += strings.Repeat("=", pad)
	}
	// jwt library uses URL-safe base64 without padding. Accept both.
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	return decodeStdB64(s)
}

func writeAuthError(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized", "reason": reason})
}
