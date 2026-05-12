package httpapi

import (
	"log/slog"
	"net/http"
)

// HTTP error helpers shared by every handler in this package.
//
// Per convention (CLAUDE.md "Code conventions"), handler code never writes
// `map[string]string{"error": ...}` inline. The envelope shape is owned by
// these helpers so changing it later is a one-file edit.
//
// Today there are two wire shapes in active use:
//
//	{"error": "human-readable message"}                  // most errors
//	{"error": "stable_code", "hint": "human message"}    // scope/agent gates
//
// Helpers cover the common cases. For unusual statuses, callers compose
// via writeErr directly.

// writeErr writes the single-string envelope at the given status.
// msg goes into "error". Use this for invalid-input / not-found /
// internal-error cases where the caller doesn't need a stable code yet.
func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// badRequest writes a 400 with a human-readable error message.
func badRequest(w http.ResponseWriter, msg string) {
	writeErr(w, http.StatusBadRequest, msg)
}

// notFound writes a 404 with a human-readable error message.
func notFound(w http.ResponseWriter, msg string) {
	writeErr(w, http.StatusNotFound, msg)
}

// unauthorized writes a 401 with a human-readable error message. Used by
// handlers that gate on auth context after the middleware has populated it
// (the middleware itself uses its own writer in internal/auth).
func unauthorized(w http.ResponseWriter, msg string) {
	writeErr(w, http.StatusUnauthorized, msg)
}

// methodNotAllowed writes a 405 and sets the Allow header.
func methodNotAllowed(w http.ResponseWriter, allow, msg string) {
	w.Header().Set("Allow", allow)
	writeErr(w, http.StatusMethodNotAllowed, msg)
}

// internalErr writes a 500 and logs the underlying err with msg + attrs.
// The wire body never carries the raw err — only the supplied msg —
// because that string ships to UI / external callers.
func internalErr(w http.ResponseWriter, logger *slog.Logger, err error, msg string, attrs ...any) {
	if logger != nil {
		logger.Error(msg, append(attrs, "err", err)...)
	}
	writeErr(w, http.StatusInternalServerError, msg)
}

// forbidden writes a 403 with a stable code + a user-facing hint. Used by
// the scope/agent gates where the request was well-formed but rejected.
func forbidden(w http.ResponseWriter, code, hint string) {
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error": code,
		"hint":  hint,
	})
}
