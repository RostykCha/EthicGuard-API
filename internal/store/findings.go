package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// pgxNoRows is aliased so the sole pgx import lives at the top of the file
// and the "errors" pair stays close to GetByIDForInstallation below.
var pgxNoRows = pgx.ErrNoRows

// FindingInput is the caller-facing shape for inserting a finding. Deliberately
// excludes any free-text "message" field — the zero-retention contract says
// persisted findings carry only the catalog key + params + anchor + rationale.
type FindingInput struct {
	Category     string
	Severity     string
	Score        int
	Anchor       Anchor
	MessageKey   string
	Params       map[string]string
	RationaleTag string // optional; one of the CHECK-constrained enums
}

// Anchor is the persisted anchor shape: a pointer into a Jira field with
// optional UTF-8 byte offsets. Zero offsets mean "whole field."
type Anchor struct {
	Field string `json:"field"`
	Start int    `json:"start,omitempty"`
	End   int    `json:"end,omitempty"`
}

// Finding is the stored row returned by ListByJob. MessageText is never a
// column — it is resolved by the catalog when the status handler serves the
// row to the UI.
type Finding struct {
	ID           int64
	JobID        int64
	Category     string
	Severity     string
	Score        int
	Anchor       Anchor
	MessageKey   string
	Params       map[string]string
	RationaleTag string
}

// Findings repository over the `findings` table.
type Findings struct {
	Store *Store
}

// Insert appends a finding row. The message_key column is NOT NULL and carries
// a stable catalog key; message text is never written here.
func (r *Findings) Insert(ctx context.Context, jobID int64, in FindingInput) error {
	anchorJSON, err := json.Marshal(in.Anchor)
	if err != nil {
		return fmt.Errorf("findings insert: marshal anchor: %w", err)
	}
	params := in.Params
	if params == nil {
		params = map[string]string{}
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("findings insert: marshal params: %w", err)
	}
	const q = `
		INSERT INTO findings (job_id, category, severity, score, anchor, message_key, params, rationale_tag)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7::jsonb, NULLIF($8, ''))
	`
	if _, err := r.Store.Pool.Exec(ctx, q, jobID, in.Category, in.Severity, in.Score, string(anchorJSON), in.MessageKey, string(paramsJSON), in.RationaleTag); err != nil {
		return fmt.Errorf("findings insert: %w", err)
	}
	return nil
}

// Severity counts keyed by severity enum value. Returned by SummaryByJob.
type SeverityCounts struct {
	High   int
	Medium int
	Low    int
	Info   int
	Total  int
}

// SummaryByJob returns the finding counts bucketed by severity for a single
// job, in one query. Powers the Confidence Ribbon.
func (r *Findings) SummaryByJob(ctx context.Context, jobID int64) (SeverityCounts, error) {
	const q = `
		SELECT
		  COALESCE(SUM(CASE WHEN severity='high'   THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN severity='medium' THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN severity='low'    THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN severity='info'   THEN 1 ELSE 0 END), 0),
		  COUNT(*)
		FROM findings WHERE job_id = $1
	`
	var s SeverityCounts
	if err := r.Store.Pool.QueryRow(ctx, q, jobID).Scan(&s.High, &s.Medium, &s.Low, &s.Info, &s.Total); err != nil {
		return SeverityCounts{}, fmt.Errorf("findings summary: %w", err)
	}
	return s, nil
}

// GetByIDForInstallation fetches a finding + its parent job id, verifying
// the job belongs to the caller's installation. Tenant isolation check used
// by the action endpoint.
func (r *Findings) GetByIDForInstallation(ctx context.Context, findingID, installationID int64) (*Finding, error) {
	const q = `
		SELECT f.id, f.job_id, f.category, f.severity, f.score, f.anchor,
		       f.message_key, f.params, COALESCE(f.rationale_tag, '')
		FROM findings f
		JOIN jobs j ON j.id = f.job_id
		WHERE f.id = $1 AND j.installation_id = $2
	`
	var (
		f         Finding
		anchorRaw []byte
		paramsRaw []byte
	)
	err := r.Store.Pool.QueryRow(ctx, q, findingID, installationID).Scan(
		&f.ID, &f.JobID, &f.Category, &f.Severity, &f.Score,
		&anchorRaw, &f.MessageKey, &paramsRaw, &f.RationaleTag,
	)
	if err != nil {
		if errors.Is(err, pgxNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("findings get: %w", err)
	}
	if err := json.Unmarshal(anchorRaw, &f.Anchor); err != nil {
		return nil, fmt.Errorf("findings get anchor: %w", err)
	}
	if len(paramsRaw) > 0 {
		if err := json.Unmarshal(paramsRaw, &f.Params); err != nil {
			return nil, fmt.Errorf("findings get params: %w", err)
		}
	}
	return &f, nil
}

// ListByJob returns every finding attached to a job in insertion order.
func (r *Findings) ListByJob(ctx context.Context, jobID int64) ([]Finding, error) {
	const q = `
		SELECT id, job_id, category, severity, score, anchor, message_key, params,
		       COALESCE(rationale_tag, '')
		FROM findings
		WHERE job_id = $1
		ORDER BY id ASC
	`
	rows, err := r.Store.Pool.Query(ctx, q, jobID)
	if err != nil {
		return nil, fmt.Errorf("findings list: %w", err)
	}
	defer rows.Close()

	var out []Finding
	for rows.Next() {
		var (
			f         Finding
			anchorRaw []byte
			paramsRaw []byte
		)
		if err := rows.Scan(&f.ID, &f.JobID, &f.Category, &f.Severity, &f.Score, &anchorRaw, &f.MessageKey, &paramsRaw, &f.RationaleTag); err != nil {
			return nil, fmt.Errorf("findings list scan: %w", err)
		}
		if err := json.Unmarshal(anchorRaw, &f.Anchor); err != nil {
			return nil, fmt.Errorf("findings list anchor: %w", err)
		}
		if len(paramsRaw) > 0 {
			if err := json.Unmarshal(paramsRaw, &f.Params); err != nil {
				return nil, fmt.Errorf("findings list params: %w", err)
			}
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("findings list rows: %w", err)
	}
	return out, nil
}
