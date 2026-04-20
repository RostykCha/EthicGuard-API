package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// UserPreference is the per-(installation, accountId) override for text
// variants. Zero-retention: persona is a fixed enum, the Jira account id is
// not customer content.
type UserPreference struct {
	ID             int64
	InstallationID int64
	AccountID      string
	Persona        string // "" | "pm" | "qa" | "dev"
}

// UserPreferences repository over the `user_preferences` table.
type UserPreferences struct {
	Store *Store
}

// Get returns the stored preference, or ErrNotFound.
func (r *UserPreferences) Get(ctx context.Context, installationID int64, accountID string) (*UserPreference, error) {
	const q = `
		SELECT id, installation_id, account_id, COALESCE(persona, '')
		FROM user_preferences
		WHERE installation_id = $1 AND account_id = $2
	`
	var pref UserPreference
	err := r.Store.Pool.QueryRow(ctx, q, installationID, accountID).Scan(
		&pref.ID, &pref.InstallationID, &pref.AccountID, &pref.Persona,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("user_preferences get: %w", err)
	}
	return &pref, nil
}

// Upsert replaces the stored preference. persona must be one of "pm", "qa",
// "dev", or "" (clears the preference).
func (r *UserPreferences) Upsert(ctx context.Context, installationID int64, accountID, persona string) (*UserPreference, error) {
	const q = `
		INSERT INTO user_preferences (installation_id, account_id, persona, created_at, updated_at)
		VALUES ($1, $2, NULLIF($3, ''), NOW(), NOW())
		ON CONFLICT (installation_id, account_id) DO UPDATE
		SET persona = EXCLUDED.persona,
		    updated_at = NOW()
		RETURNING id, installation_id, account_id, COALESCE(persona, '')
	`
	row := r.Store.Pool.QueryRow(ctx, q, installationID, accountID, persona)
	var pref UserPreference
	if err := row.Scan(&pref.ID, &pref.InstallationID, &pref.AccountID, &pref.Persona); err != nil {
		return nil, fmt.Errorf("user_preferences upsert: %w", err)
	}
	return &pref, nil
}
