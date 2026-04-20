package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// Audit repository over the `audit_log` table. Zero-retention: `meta` JSONB
// carries only non-content metadata (counts, durations, enums, ids).
type Audit struct {
	Store *Store
}

// Log appends an audit row. `actorAccountID` may be empty for system actions.
// `target` is typically an issue key or job id string. `meta` is marshalled
// to JSON; pass nil for none.
func (r *Audit) Log(ctx context.Context, installationID int64, actorAccountID, action, target string, meta map[string]any) error {
	var metaJSON []byte
	if meta != nil {
		b, err := json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("audit log marshal: %w", err)
		}
		metaJSON = b
	}
	const q = `
		INSERT INTO audit_log (installation_id, actor_account_id, action, target, meta)
		VALUES ($1, NULLIF($2, ''), $3, NULLIF($4, ''), $5::jsonb)
	`
	var metaArg any
	if metaJSON != nil {
		metaArg = string(metaJSON)
	}
	if _, err := r.Store.Pool.Exec(ctx, q, installationID, actorAccountID, action, target, metaArg); err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	return nil
}
