package store

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"direct ErrNotFound", ErrNotFound, true},
		{"wrapped ErrNotFound", fmt.Errorf("wrap: %w", ErrNotFound), true},
		{"double-wrapped", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", ErrNotFound)), true},
		{"unrelated error", errors.New("something else"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNotFound(tc.err); got != tc.want {
				t.Errorf("IsNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
