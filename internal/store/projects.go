package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ProjectConfig is the slice of per-project configuration EthicGuard cares
// about. The struct is the extension point as per-project policy grows.
//
// Zero-retention: every field here is configuration metadata, not issue
// content. `TestedIssueTypes` holds Jira issue-type IDs (short numeric
// strings); the agent-* fields are admin-authored knobs.
type ProjectConfig struct {
	ProjectKey             string
	TestedIssueTypes       []string
	AgentEnabled           bool
	AgentSeverityThreshold string // "info" | "low" | "medium" | "high"
	AgentPromptAddendum    string
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
		SELECT project_key, tested_issue_types,
		       agent_enabled, agent_severity_threshold, agent_prompt_addendum
		FROM projects
		WHERE installation_id = $1 AND project_key = $2
	`
	row := r.Store.Pool.QueryRow(ctx, q, installationID, projectKey)
	cfg := &ProjectConfig{}
	if err := row.Scan(
		&cfg.ProjectKey, &cfg.TestedIssueTypes,
		&cfg.AgentEnabled, &cfg.AgentSeverityThreshold, &cfg.AgentPromptAddendum,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("projects get config: %w", err)
	}
	return cfg, nil
}

// SetConfig upserts the project row with the full admin-settable config:
// the issue-type allow-list and the three agent-config knobs. Empty
// TestedIssueTypes is allowed (admin paused the app for this project).
func (r *Projects) SetConfig(ctx context.Context, installationID int64, projectKey string, in ProjectConfig) (*ProjectConfig, error) {
	types := in.TestedIssueTypes
	if types == nil {
		types = []string{}
	}
	const q = `
		INSERT INTO projects (
			installation_id, project_key,
			tested_issue_types,
			agent_enabled, agent_severity_threshold, agent_prompt_addendum
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (installation_id, project_key) DO UPDATE
		SET tested_issue_types        = EXCLUDED.tested_issue_types,
		    agent_enabled             = EXCLUDED.agent_enabled,
		    agent_severity_threshold  = EXCLUDED.agent_severity_threshold,
		    agent_prompt_addendum     = EXCLUDED.agent_prompt_addendum
		RETURNING project_key, tested_issue_types,
		          agent_enabled, agent_severity_threshold, agent_prompt_addendum
	`
	row := r.Store.Pool.QueryRow(ctx, q,
		installationID, projectKey,
		types,
		in.AgentEnabled, in.AgentSeverityThreshold, in.AgentPromptAddendum,
	)
	cfg := &ProjectConfig{}
	if err := row.Scan(
		&cfg.ProjectKey, &cfg.TestedIssueTypes,
		&cfg.AgentEnabled, &cfg.AgentSeverityThreshold, &cfg.AgentPromptAddendum,
	); err != nil {
		return nil, fmt.Errorf("projects set config: %w", err)
	}
	return cfg, nil
}
