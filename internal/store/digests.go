package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Digest is a weekly snapshot of notable findings for an installation. Only
// finding ids are stored; message text is re-rendered via the catalog when
// the UI fetches the latest digest.
type Digest struct {
	ID             int64
	InstallationID int64
	PeriodStart    time.Time
	PeriodEnd      time.Time
	FindingIDs     []int64
	CreatedAt      time.Time
}

// Digests repository over the `digests` table.
type Digests struct {
	Store *Store
}

// Insert appends a new digest row.
func (r *Digests) Insert(ctx context.Context, installationID int64, periodStart, periodEnd time.Time, findingIDs []int64) (*Digest, error) {
	const q = `
		INSERT INTO digests (installation_id, period_start, period_end, finding_ids, created_at)
		VALUES ($1, $2, $3, $4, NOW())
		RETURNING id, installation_id, period_start, period_end, finding_ids, created_at
	`
	row := r.Store.Pool.QueryRow(ctx, q, installationID, periodStart, periodEnd, findingIDs)
	var d Digest
	if err := row.Scan(&d.ID, &d.InstallationID, &d.PeriodStart, &d.PeriodEnd, &d.FindingIDs, &d.CreatedAt); err != nil {
		return nil, fmt.Errorf("digests insert: %w", err)
	}
	return &d, nil
}

// GetLatest returns the most recent digest for an installation, or
// ErrNotFound if none have been generated yet.
func (r *Digests) GetLatest(ctx context.Context, installationID int64) (*Digest, error) {
	const q = `
		SELECT id, installation_id, period_start, period_end, finding_ids, created_at
		FROM digests
		WHERE installation_id = $1
		ORDER BY created_at DESC
		LIMIT 1
	`
	row := r.Store.Pool.QueryRow(ctx, q, installationID)
	var d Digest
	if err := row.Scan(&d.ID, &d.InstallationID, &d.PeriodStart, &d.PeriodEnd, &d.FindingIDs, &d.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("digests get latest: %w", err)
	}
	return &d, nil
}

// ListInstallationIDs returns the id of every active installation; used by
// the digest scheduler to iterate over all tenants.
func (r *Digests) ListInstallationIDs(ctx context.Context) ([]int64, error) {
	const q = `SELECT id FROM installations ORDER BY id`
	rows, err := r.Store.Pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("digests list installations: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("digests list installations scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CandidateFindingsForDigest returns the top-N "interesting" finding ids for
// one installation over a time window. "Interesting" is defined as either
// having a rationale_tag that signals depth, or being in the
// missing_negative_case category — the ones a human reviewer is most likely
// to have missed, sorted by score DESC.
func (r *Digests) CandidateFindingsForDigest(ctx context.Context, installationID int64, since, until time.Time, limit int) ([]int64, error) {
	const q = `
		SELECT f.id
		FROM findings f
		JOIN jobs j ON j.id = f.job_id
		WHERE j.installation_id = $1
		  AND f.created_at >= $2
		  AND f.created_at <  $3
		  AND (
		    f.rationale_tag IN ('spec_conflict', 'assumption_gap', 'missing_negative')
		    OR f.category = 'missing_negative_case'
		  )
		ORDER BY f.score DESC, f.created_at DESC
		LIMIT $4
	`
	rows, err := r.Store.Pool.Query(ctx, q, installationID, since, until, limit)
	if err != nil {
		return nil, fmt.Errorf("digests candidate findings: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("digests candidate findings scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ResolveFindingsForDigest returns the full finding rows (+ issue key) for
// the ids stored on a digest row, in the same order.
type DigestFinding struct {
	Finding  Finding
	IssueKey string
}

func (r *Digests) ResolveFindingsForDigest(ctx context.Context, installationID int64, findingIDs []int64) ([]DigestFinding, error) {
	if len(findingIDs) == 0 {
		return nil, nil
	}
	const q = `
		SELECT f.id, f.job_id, f.category, f.severity, f.score, f.anchor,
		       f.message_key, f.params, COALESCE(f.rationale_tag, ''), j.issue_key
		FROM findings f
		JOIN jobs j ON j.id = f.job_id
		WHERE j.installation_id = $1 AND f.id = ANY($2)
	`
	rows, err := r.Store.Pool.Query(ctx, q, installationID, findingIDs)
	if err != nil {
		return nil, fmt.Errorf("digests resolve findings: %w", err)
	}
	defer rows.Close()
	byID := map[int64]DigestFinding{}
	for rows.Next() {
		var (
			f         Finding
			issueKey  string
			anchorRaw []byte
			paramsRaw []byte
		)
		if err := rows.Scan(
			&f.ID, &f.JobID, &f.Category, &f.Severity, &f.Score,
			&anchorRaw, &f.MessageKey, &paramsRaw, &f.RationaleTag, &issueKey,
		); err != nil {
			return nil, fmt.Errorf("digests resolve findings scan: %w", err)
		}
		if err := unmarshalJSONBytes(anchorRaw, &f.Anchor); err != nil {
			return nil, err
		}
		if err := unmarshalJSONBytes(paramsRaw, &f.Params); err != nil {
			return nil, err
		}
		byID[f.ID] = DigestFinding{Finding: f, IssueKey: issueKey}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("digests resolve findings rows: %w", err)
	}
	// Preserve the order in which ids appear on the digest row.
	out := make([]DigestFinding, 0, len(findingIDs))
	for _, id := range findingIDs {
		if df, ok := byID[id]; ok {
			out = append(out, df)
		}
	}
	return out, nil
}
