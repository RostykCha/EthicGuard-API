package store

import (
	"context"
	"embed"
	"fmt"
	"log/slog"

	"github.com/pressly/goose/v3"
)

// MigrationsFS embeds the SQL migrations so the binary is self-contained.
// Migrations live under the repo's top-level `migrations/` directory.
//
//go:embed migrations/*.sql
var MigrationsFS embed.FS

// Migrate runs all pending migrations against the given store. It is safe to
// call on every boot — goose tracks applied migrations in `goose_db_version`.
func (s *Store) Migrate(ctx context.Context, logger *slog.Logger) error {
	db := s.SQLDB()
	defer func() { _ = db.Close() }()

	goose.SetBaseFS(MigrationsFS)
	goose.SetLogger(gooseSlogAdapter{logger: logger})

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("store: set dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("store: migrate up: %w", err)
	}
	return nil
}

// gooseSlogAdapter bridges goose's logger interface to slog.
type gooseSlogAdapter struct {
	logger *slog.Logger
}

func (g gooseSlogAdapter) Printf(format string, v ...any) {
	g.logger.Info(fmt.Sprintf(format, v...), "component", "goose")
}

func (g gooseSlogAdapter) Fatalf(format string, v ...any) {
	g.logger.Error(fmt.Sprintf(format, v...), "component", "goose")
}
