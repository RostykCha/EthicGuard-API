// Package auth verifies Forge-minted JWTs from EthicGuard-UI and resolves the
// calling installation by cloudId.
//
// The Forge UI mints a short-lived HS256 JWT in a resolver using a
// per-installation shared secret stored here on install. The API verifies the
// signature, checks the audience and expiry, and loads the installations row.
// No user tokens are accepted.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims are the fields EthicGuard cares about from a Forge-minted JWT.
// Cloud ID is set by the Forge resolver from its runtime context and used to
// look up the per-installation shared secret for verification.
type Claims struct {
	CloudID  string
	Audience string
	IssuedAt int64
	Expires  int64
}

// Subject encodes which audience the token is for. Two audiences exist:
//
//   - "ethicguard-api"       — normal user-initiated requests from the UI
//   - "ethicguard-installer" — lifecycle webhook bootstrap, signed with the
//     pre-shared INSTALLER_SECRET (no per-installation secret yet)
const (
	AudienceAPI       = "ethicguard-api"
	AudienceInstaller = "ethicguard-installer"
)

// ErrInvalidToken is returned when verification fails for any reason.
var ErrInvalidToken = errors.New("auth: invalid token")

// Verify parses and validates an HS256 JWT against the given signing secret.
// It enforces expiry, issued-at, and that the audience matches expectedAud.
// Returns normalized Claims on success.
func Verify(tokenString, signingSecret, expectedAud string) (*Claims, error) {
	if tokenString == "" || signingSecret == "" {
		return nil, ErrInvalidToken
	}
	parsed, err := jwt.Parse(tokenString, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(signingSecret), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithAudience(expectedAud))
	if err != nil || !parsed.Valid {
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	mc, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, ErrInvalidToken
	}

	cloudID, _ := mc["cloudId"].(string)
	if cloudID == "" {
		return nil, fmt.Errorf("%w: missing cloudId", ErrInvalidToken)
	}
	out := &Claims{CloudID: cloudID, Audience: expectedAud}
	if v, ok := mc["iat"].(float64); ok {
		out.IssuedAt = int64(v)
	}
	if v, ok := mc["exp"].(float64); ok {
		out.Expires = int64(v)
	}
	// Reject tokens issued too far in the future (clock skew tolerance: 60s).
	if out.IssuedAt > time.Now().Unix()+60 {
		return nil, fmt.Errorf("%w: iat in future", ErrInvalidToken)
	}
	return out, nil
}
