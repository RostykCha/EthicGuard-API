package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Installation is the stored record for a Jira Cloud installation of the
// Forge app. Zero-retention rule: this row carries no issue content — only
// the cloudId, the per-install HS256 shared secret used to verify JWTs from
// the Forge app, and timestamps.
type Installation struct {
	ID           int64
	CloudID      string
	SharedSecret string
}

// Installations repository over the `installations` table.
type Installations struct {
	Store *Store
}

// Upsert creates or replaces the shared secret for a cloudId. Called from the
// lifecycle webhook on install and on secret rotation.
func (r *Installations) Upsert(ctx context.Context, cloudID, sharedSecret string) (*Installation, error) {
	const q = `
		INSERT INTO installations (cloud_id, shared_secret, installed_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (cloud_id) DO UPDATE
		SET shared_secret = EXCLUDED.shared_secret,
		    updated_at    = NOW()
		RETURNING id, cloud_id, shared_secret
	`
	row := r.Store.Pool.QueryRow(ctx, q, cloudID, sharedSecret)
	inst := &Installation{}
	if err := row.Scan(&inst.ID, &inst.CloudID, &inst.SharedSecret); err != nil {
		return nil, fmt.Errorf("installations upsert: %w", err)
	}
	return inst, nil
}

// GetByCloudID returns the installation for a given cloudId, or ErrNotFound.
func (r *Installations) GetByCloudID(ctx context.Context, cloudID string) (*Installation, error) {
	const q = `SELECT id, cloud_id, shared_secret FROM installations WHERE cloud_id = $1`
	row := r.Store.Pool.QueryRow(ctx, q, cloudID)
	inst := &Installation{}
	if err := row.Scan(&inst.ID, &inst.CloudID, &inst.SharedSecret); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("installations get: %w", err)
	}
	return inst, nil
}

// DeleteByCloudID removes an installation and cascades to all dependent rows
// (projects, jobs, findings, conflicts, audit_log) per the schema's ON DELETE
// CASCADE. Called from the lifecycle webhook on uninstall — this is the
// zero-retention "nothing recoverable after uninstall" guarantee.
func (r *Installations) DeleteByCloudID(ctx context.Context, cloudID string) error {
	const q = `DELETE FROM installations WHERE cloud_id = $1`
	tag, err := r.Store.Pool.Exec(ctx, q, cloudID)
	if err != nil {
		return fmt.Errorf("installations delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
