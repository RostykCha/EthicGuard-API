package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Project is the stored record for a Jira project scoped to one installation.
type Project struct {
	ID                  int64
	InstallationID      int64
	ProjectKey          string
	ConfidenceThreshold int
	ThresholdOverrides  map[string]int // category → override threshold (0-100)
}

// Projects repository over the `projects` table.
type Projects struct {
	Store *Store
}

// UpsertByKey returns the project id for (installationID, projectKey),
// creating the row if needed. Idempotent; safe to call from every POST.
func (r *Projects) UpsertByKey(ctx context.Context, installationID int64, projectKey string) (int64, error) {
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

// GetByKey returns the full project row (including thresholds).
func (r *Projects) GetByKey(ctx context.Context, installationID int64, projectKey string) (*Project, error) {
	const q = `
		SELECT id, installation_id, project_key, confidence_threshold, threshold_overrides
		FROM projects
		WHERE installation_id = $1 AND project_key = $2
	`
	var p Project
	var overridesRaw []byte
	err := r.Store.Pool.QueryRow(ctx, q, installationID, projectKey).Scan(
		&p.ID, &p.InstallationID, &p.ProjectKey, &p.ConfidenceThreshold, &overridesRaw,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("projects get: %w", err)
	}
	if len(overridesRaw) > 0 {
		if err := json.Unmarshal(overridesRaw, &p.ThresholdOverrides); err != nil {
			return nil, fmt.Errorf("projects get overrides: %w", err)
		}
	}
	if p.ThresholdOverrides == nil {
		p.ThresholdOverrides = map[string]int{}
	}
	return &p, nil
}

// GetByID returns the full project row by id.
func (r *Projects) GetByID(ctx context.Context, id int64) (*Project, error) {
	const q = `
		SELECT id, installation_id, project_key, confidence_threshold, threshold_overrides
		FROM projects
		WHERE id = $1
	`
	var p Project
	var overridesRaw []byte
	err := r.Store.Pool.QueryRow(ctx, q, id).Scan(
		&p.ID, &p.InstallationID, &p.ProjectKey, &p.ConfidenceThreshold, &overridesRaw,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("projects get by id: %w", err)
	}
	if len(overridesRaw) > 0 {
		if err := json.Unmarshal(overridesRaw, &p.ThresholdOverrides); err != nil {
			return nil, fmt.Errorf("projects get overrides: %w", err)
		}
	}
	if p.ThresholdOverrides == nil {
		p.ThresholdOverrides = map[string]int{}
	}
	return &p, nil
}

// UpdateThreshold sets the per-project confidence floor (0-100). Upserts so
// a project set via POST /v1/analysis can have its threshold configured
// before any analysis has ever run.
func (r *Projects) UpdateThreshold(ctx context.Context, installationID int64, projectKey string, threshold int) error {
	const q = `
		INSERT INTO projects (installation_id, project_key, confidence_threshold)
		VALUES ($1, $2, $3)
		ON CONFLICT (installation_id, project_key) DO UPDATE
		SET confidence_threshold = EXCLUDED.confidence_threshold
	`
	if _, err := r.Store.Pool.Exec(ctx, q, installationID, projectKey, threshold); err != nil {
		return fmt.Errorf("projects update threshold: %w", err)
	}
	return nil
}

// SetOverrides replaces threshold_overrides for one project. Written by the
// dismissal learning loop (Phase 2 #7). Passing an empty map clears all
// per-category overrides.
func (r *Projects) SetOverrides(ctx context.Context, projectID int64, overrides map[string]int) error {
	if overrides == nil {
		overrides = map[string]int{}
	}
	body, err := json.Marshal(overrides)
	if err != nil {
		return fmt.Errorf("projects overrides marshal: %w", err)
	}
	const q = `UPDATE projects SET threshold_overrides = $2::jsonb WHERE id = $1`
	tag, err := r.Store.Pool.Exec(ctx, q, projectID, string(body))
	if err != nil {
		return fmt.Errorf("projects set overrides: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// EffectiveThreshold returns the floor for a given (project, category). The
// per-category override wins if set; otherwise the project-wide threshold.
func (p *Project) EffectiveThreshold(category string) int {
	if p == nil {
		return 0
	}
	if v, ok := p.ThresholdOverrides[category]; ok {
		return v
	}
	return p.ConfidenceThreshold
}
