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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// ErrNotFound is returned by repositories when a row lookup misses.
var ErrNotFound = errors.New("store: not found")

// Querier is the narrow slice of pgxpool.Pool the repositories actually
// use. Declared here so tests can substitute pgxmock without touching
// production code. The interface deliberately stays minimal — adding a
// method here means every repo can depend on it, so keep it small.
//
// Both *pgxpool.Pool (production) and *pgxmock.PgxPoolIface (tests)
// satisfy these signatures.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Store owns the pgx connection pool shared across all repositories.
//
// DB is what repositories use for queries — typed as Querier so tests
// can fake it. pool is the concrete pgx pool, retained for SQLDB() which
// is called only by Migrate() and requires the real pgx implementation.
// In test setups that build Store{DB: pgxmock.NewPool()}, pool is nil
// and SQLDB() must not be called.
type Store struct {
	DB   Querier
	pool *pgxpool.Pool
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
	return &Store{DB: pool, pool: pool}, nil
}

// Close releases pool resources. No-op when the store was constructed with
// a fake (pool is nil in tests using pgxmock).
func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// SQLDB returns a *sql.DB view of the pool, required by goose which speaks
// database/sql. The returned handle shares the underlying pgx connections via
// pgx's stdlib driver — callers must not Close() it. Panics if Store was
// constructed without a concrete pool (test setups using pgxmock should
// bypass Migrate and never call SQLDB).
func (s *Store) SQLDB() *sql.DB {
	if s.pool == nil {
		panic("store: SQLDB requires a concrete pgxpool.Pool (not available in test fakes)")
	}
	return stdlib.OpenDBFromPool(s.pool)
}

// LogAttrs returns slog attrs useful when logging around store operations.
func LogAttrs(logger *slog.Logger) *slog.Logger {
	return logger.With("component", "store")
}
