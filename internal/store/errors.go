package store

import "errors"

// IsNotFound reports whether err is or wraps ErrNotFound. Use this instead of
// `errors.Is(err, store.ErrNotFound)` at call sites — same behavior, less
// boilerplate, one spelling to grep for across the codebase.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
