// Package auth verifies Forge-minted JWTs from EthicGuard-UI and resolves the
// calling installation by cloudId.
//
// The Forge UI mints a short-lived JWT in a resolver using a per-installation
// shared secret stored here on install. The API verifies the signature, checks
// the audience, and loads the installation row. No user tokens are accepted.
package auth

import "errors"

// ErrNotImplemented is returned by stubs until the real verifier lands.
var ErrNotImplemented = errors.New("auth: not implemented")

// Claims are the fields EthicGuard cares about from a Forge-minted JWT.
type Claims struct {
	CloudID  string
	Audience string
	IssuedAt int64
}

// Verifier validates a JWT and returns its claims.
type Verifier interface {
	Verify(token string) (*Claims, error)
}
