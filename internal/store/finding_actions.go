package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// FindingAction is a user's recorded response to a finding.
type FindingAction struct {
	ID             int64
	FindingID      int64
	Action         string // "accept" | "dismiss"
	Reason         string // "" for accept; one of the enums for dismiss
	ActorAccountID string
	CreatedAt      time.Time
}

// FindingActions repository over the `finding_actions` table.
type FindingActions struct {
	Store *Store
}

// Upsert records the user's action on a finding. The UNIQUE(finding_id)
// constraint means each finding carries at most one action; subsequent
// calls replace the prior choice (e.g. accept → dismiss after second look).
// Returns ErrNotFound if the finding does not belong to the caller's
// installation — that check is done in the handler, not here.
func (r *FindingActions) Upsert(ctx context.Context, findingID int64, action, reason, actorAccountID string) (*FindingAction, error) {
	const q = `
		INSERT INTO finding_actions (finding_id, action, reason, actor_account_id, created_at)
		VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), NOW())
		ON CONFLICT (finding_id) DO UPDATE
		SET action = EXCLUDED.action,
		    reason = EXCLUDED.reason,
		    actor_account_id = EXCLUDED.actor_account_id,
		    created_at = NOW()
		RETURNING id, finding_id, action, COALESCE(reason, ''), COALESCE(actor_account_id, ''), created_at
	`
	row := r.Store.Pool.QueryRow(ctx, q, findingID, action, reason, actorAccountID)
	fa := &FindingAction{}
	if err := row.Scan(&fa.ID, &fa.FindingID, &fa.Action, &fa.Reason, &fa.ActorAccountID, &fa.CreatedAt); err != nil {
		return nil, fmt.Errorf("finding_actions upsert: %w", err)
	}
	return fa, nil
}

// GetByFinding returns the recorded action for a finding, or ErrNotFound.
func (r *FindingActions) GetByFinding(ctx context.Context, findingID int64) (*FindingAction, error) {
	const q = `
		SELECT id, finding_id, action, COALESCE(reason, ''), COALESCE(actor_account_id, ''), created_at
		FROM finding_actions
		WHERE finding_id = $1
	`
	row := r.Store.Pool.QueryRow(ctx, q, findingID)
	fa := &FindingAction{}
	if err := row.Scan(&fa.ID, &fa.FindingID, &fa.Action, &fa.Reason, &fa.ActorAccountID, &fa.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("finding_actions get: %w", err)
	}
	return fa, nil
}

// ListByJob returns every action attached to a job's findings. Keyed by
// finding id for easy merge with the findings list on the UI side.
func (r *FindingActions) ListByJob(ctx context.Context, jobID int64) (map[int64]*FindingAction, error) {
	const q = `
		SELECT fa.id, fa.finding_id, fa.action, COALESCE(fa.reason, ''),
		       COALESCE(fa.actor_account_id, ''), fa.created_at
		FROM finding_actions fa
		JOIN findings f ON f.id = fa.finding_id
		WHERE f.job_id = $1
	`
	rows, err := r.Store.Pool.Query(ctx, q, jobID)
	if err != nil {
		return nil, fmt.Errorf("finding_actions list: %w", err)
	}
	defer rows.Close()

	out := map[int64]*FindingAction{}
	for rows.Next() {
		fa := &FindingAction{}
		if err := rows.Scan(&fa.ID, &fa.FindingID, &fa.Action, &fa.Reason, &fa.ActorAccountID, &fa.CreatedAt); err != nil {
			return nil, fmt.Errorf("finding_actions list scan: %w", err)
		}
		out[fa.FindingID] = fa
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("finding_actions list rows: %w", err)
	}
	return out, nil
}
