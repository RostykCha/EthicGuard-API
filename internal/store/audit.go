package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// Audits repository over the `audit_log` table. Every config-changing admin
// action goes through this so we have a tamper-evident trail of who changed
// what and when. Zero-retention: meta must never carry issue content — only
// configuration deltas, ids, and enum values.
type Audits struct {
	Store *Store
}

// Log writes one audit row. actorAccountID may be empty for system-driven
// actions (e.g., a worker auto-action). meta may be nil; when present it is
// serialized as JSONB.
func (r *Audits) Log(ctx context.Context, installationID int64, actorAccountID, action, target string, meta map[string]any) error {
	var metaJSON []byte
	if meta != nil {
		b, err := json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("audit log marshal meta: %w", err)
		}
		metaJSON = b
	}
	const q = `
		INSERT INTO audit_log (installation_id, actor_account_id, action, target, meta)
		VALUES ($1, NULLIF($2, ''), $3, NULLIF($4, ''), $5)
	`
	if _, err := r.Store.Pool.Exec(ctx, q, installationID, actorAccountID, action, target, metaJSON); err != nil {
		return fmt.Errorf("audit log insert: %w", err)
	}
	return nil
}
