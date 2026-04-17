// Package store owns all Postgres access for EthicGuard-API.
//
// Zero-retention rule: repositories in this package must never persist Jira
// issue content (title, body, description, AC text, comments). Stored rows
// carry only ids, scores, anchors, and references.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// ErrNotFound is returned by repositories when a row lookup misses.
var ErrNotFound = errors.New("store: not found")

// Store owns the pgx connection pool shared across all repositories.
type Store struct {
	Pool *pgxpool.Pool
}

// Open dials Postgres, pings it, and returns a ready-to-use Store. The caller
// owns lifecycle; call Close() on shutdown.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	if databaseURL == "" {
		return nil, errors.New("store: database URL is empty")
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: parse database url: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: open pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{Pool: pool}, nil
}

// Close releases pool resources.
func (s *Store) Close() {
	if s != nil && s.Pool != nil {
		s.Pool.Close()
	}
}

// SQLDB returns a *sql.DB view of the pool, required by goose which speaks
// database/sql. The returned handle shares the underlying pgx connections via
// pgx's stdlib driver — callers must not Close() it.
func (s *Store) SQLDB() *sql.DB {
	return stdlib.OpenDBFromPool(s.Pool)
}

// LogAttrs returns slog attrs useful when logging around store operations.
func LogAttrs(logger *slog.Logger) *slog.Logger {
	return logger.With("component", "store")
}
