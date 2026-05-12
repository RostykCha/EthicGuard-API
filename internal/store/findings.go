package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// PersistedFinding is what we write to `findings`: an anchor + a stable
// message_key. The human-readable text is NOT persisted (zero-retention) —
// it's resolved from a static catalog at render time.
//
// AGENT-NOTE: This struct is the zero-retention boundary. If you find
// yourself wanting to add `Message string`, `Description string`, or any
// other free-form text field, stop and re-read CLAUDE.md → "The
// non-negotiable rule". The right answer is almost always "add a new
// message_key to internal/analysis/catalog.go" and let the UI re-fetch
// live Jira content for context.
type PersistedFinding struct {
	ID         int64
	JobID      int64
	Category   string
	Severity   string
	Score      int
	Anchor     map[string]any
	MessageKey string
	CreatedAt  time.Time
}

// Findings repository over the `findings` table.
type Findings struct {
	Store *Store
}

// InsertBatch writes all findings for a single job in one statement. Empty
// input is a no-op (some analyses legitimately produce zero findings).
func (r *Findings) InsertBatch(ctx context.Context, jobID int64, findings []PersistedFinding) error {
	if len(findings) == 0 {
		return nil
	}
	// pgx unnest pattern: build parallel arrays for each column, send one
	// INSERT. Cheaper than N round-trips, simpler than COPY for a handful
	// of rows per job.
	categories := make([]string, len(findings))
	severities := make([]string, len(findings))
	scores := make([]int32, len(findings))
	anchors := make([][]byte, len(findings))
	messageKeys := make([]string, len(findings))
	for i, f := range findings {
		categories[i] = f.Category
		severities[i] = f.Severity
		scores[i] = int32(f.Score)
		b, err := json.Marshal(f.Anchor)
		if err != nil {
			return fmt.Errorf("findings marshal anchor: %w", err)
		}
		anchors[i] = b
		messageKeys[i] = f.MessageKey
	}
	const q = `
		INSERT INTO findings (job_id, category, severity, score, anchor, message_key)
		SELECT $1, c, s, sc, a::jsonb, m
		FROM UNNEST($2::TEXT[], $3::TEXT[], $4::SMALLINT[], $5::TEXT[], $6::TEXT[])
		     AS u(c, s, sc, a, m)
	`
	if _, err := r.Store.Pool.Exec(ctx, q, jobID, categories, severities, scores, anchors, messageKeys); err != nil {
		return fmt.Errorf("findings insert: %w", err)
	}
	return nil
}

// ListByJob returns the findings rows for a job in insertion order.
func (r *Findings) ListByJob(ctx context.Context, jobID int64) ([]PersistedFinding, error) {
	const q = `
		SELECT id, job_id, category, severity, score, anchor, message_key, created_at
		FROM findings
		WHERE job_id = $1
		ORDER BY id
	`
	rows, err := r.Store.Pool.Query(ctx, q, jobID)
	if err != nil {
		return nil, fmt.Errorf("findings list: %w", err)
	}
	defer rows.Close()

	out := []PersistedFinding{}
	for rows.Next() {
		var f PersistedFinding
		var anchorJSON []byte
		if err := rows.Scan(&f.ID, &f.JobID, &f.Category, &f.Severity, &f.Score, &anchorJSON, &f.MessageKey, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("findings scan: %w", err)
		}
		if len(anchorJSON) > 0 {
			if err := json.Unmarshal(anchorJSON, &f.Anchor); err != nil {
				return nil, fmt.Errorf("findings unmarshal anchor: %w", err)
			}
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("findings rows: %w", err)
	}
	return out, nil
}
