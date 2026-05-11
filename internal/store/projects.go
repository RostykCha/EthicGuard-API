package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ProjectConfig is the slice of per-project configuration EthicGuard cares
// about. Currently just the issue-type allow-list ("which Jira issue types
// participate in AC analysis"), but the struct is the extension point as
// per-project policy grows (model selection, severity thresholds, etc.).
//
// Zero-retention: every field here is configuration metadata, not issue
// content. `TestedIssueTypes` holds Jira issue-type IDs (short numeric
// strings), never issue text.
type ProjectConfig struct {
	ProjectKey       string
	TestedIssueTypes []string
}

// Projects repository over the `projects` table.
type Projects struct {
	Store *Store
}

// Upsert creates or returns the project row id for (installationID, projectKey).
// Used by the jobs path before enqueuing — every job needs a project FK.
func (r *Projects) Upsert(ctx context.Context, installationID int64, projectKey string) (int64, error) {
	const q = `
		INSERT INTO projects (installation_id, project_key)
		VALUES ($1, $2)
		ON CONFLICT (installation_id, project_key) DO UPDATE
		SET project_key = EXCLUDED.project_key
		RETURNING id
	`
	var id int64
	if err := r.Store.Pool.QueryRow(ctx, q, installationID, projectKey).Scan(&id); err != nil {
		return 0, fmt.Errorf("projects upsert: %w", err)
	}
	return id, nil
}

// GetConfig returns the per-project config for an installation. Returns
// ErrNotFound when no row exists yet — the caller decides whether to treat
// that as "default empty config" or surface a 404.
func (r *Projects) GetConfig(ctx context.Context, installationID int64, projectKey string) (*ProjectConfig, error) {
	const q = `
		SELECT project_key, tested_issue_types
		FROM projects
		WHERE installation_id = $1 AND project_key = $2
	`
	row := r.Store.Pool.QueryRow(ctx, q, installationID, projectKey)
	cfg := &ProjectConfig{}
	if err := row.Scan(&cfg.ProjectKey, &cfg.TestedIssueTypes); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("projects get config: %w", err)
	}
	return cfg, nil
}

// SetTestedIssueTypes upserts the project row and writes the given list of
// Jira issue-type IDs as the analysis allow-list. Empty slice is allowed and
// means "no issue types in scope" (admin paused the app for this project).
func (r *Projects) SetTestedIssueTypes(ctx context.Context, installationID int64, projectKey string, types []string) (*ProjectConfig, error) {
	if types == nil {
		types = []string{}
	}
	const q = `
		INSERT INTO projects (installation_id, project_key, tested_issue_types)
		VALUES ($1, $2, $3)
		ON CONFLICT (installation_id, project_key) DO UPDATE
		SET tested_issue_types = EXCLUDED.tested_issue_types
		RETURNING project_key, tested_issue_types
	`
	row := r.Store.Pool.QueryRow(ctx, q, installationID, projectKey, types)
	cfg := &ProjectConfig{}
	if err := row.Scan(&cfg.ProjectKey, &cfg.TestedIssueTypes); err != nil {
		return nil, fmt.Errorf("projects set tested types: %w", err)
	}
	return cfg, nil
}
