// Package store owns all Postgres access for EthicGuard-API.
//
// Zero-retention rule: repositories in this package must never persist Jira
// issue content (title, body, description, AC text, comments). Stored rows
// carry only ids, scores, anchors, and references.
package store

import "errors"

// ErrNotImplemented is returned by stubs until the real repositories land.
var ErrNotImplemented = errors.New("store: not implemented")
