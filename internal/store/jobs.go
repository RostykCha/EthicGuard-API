package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// JobStatus enumerates the lifecycle states of an analysis job.
type JobStatus string

const (
	JobQueued  JobStatus = "queued"
	JobRunning JobStatus = "running"
	JobDone    JobStatus = "done"
	JobFailed  JobStatus = "failed"
)

// Job is the stored form of an analysis request. Zero-retention rule:
// this row carries no issue content — only identifiers and lifecycle state.
type Job struct {
	ID                   int64
	InstallationID       int64
	ProjectID            int64
	IssueKey             string
	Kind                 string
	Status               JobStatus
	Error                string
	RequestedByAccountID string
	CreatedAt            time.Time
	StartedAt            *time.Time
	FinishedAt           *time.Time
}

// Jobs repository over the `jobs` table.
type Jobs struct {
	Store *Store
}

// Enqueue inserts a fresh job row in status='queued' and returns its id.
// The payload stays in the POST handler's memory — never written here.
func (r *Jobs) Enqueue(ctx context.Context, installationID, projectID int64, issueKey, kind, requestedByAccountID string) (int64, error) {
	const q = `
		INSERT INTO jobs (installation_id, project_id, issue_key, kind, status, requested_by_account_id, created_at)
		VALUES ($1, $2, $3, $4, 'queued', NULLIF($5, ''), NOW())
		RETURNING id
	`
	var id int64
	if err := r.Store.Pool.QueryRow(ctx, q, installationID, projectID, issueKey, kind, requestedByAccountID).Scan(&id); err != nil {
		return 0, fmt.Errorf("jobs enqueue: %w", err)
	}
	return id, nil
}

// GetByIDForInstallation loads a job scoped to the caller's installation,
// returning ErrNotFound if the id belongs to a different tenant or doesn't
// exist. This is the tenant-isolation check for GET /v1/analysis/{jobId}.
func (r *Jobs) GetByIDForInstallation(ctx context.Context, jobID, installationID int64) (*Job, error) {
	const q = `
		SELECT id, installation_id, project_id, issue_key, kind, status,
		       COALESCE(error, ''), COALESCE(requested_by_account_id, ''),
		       created_at, started_at, finished_at
		FROM jobs
		WHERE id = $1 AND installation_id = $2
	`
	row := r.Store.Pool.QueryRow(ctx, q, jobID, installationID)
	j := &Job{}
	if err := row.Scan(
		&j.ID, &j.InstallationID, &j.ProjectID, &j.IssueKey, &j.Kind, &j.Status,
		&j.Error, &j.RequestedByAccountID,
		&j.CreatedAt, &j.StartedAt, &j.FinishedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("jobs get: %w", err)
	}
	return j, nil
}

// MarkRunning atomically flips a queued job to running. Returns ErrNotFound
// if the job is not queued any more — a worker lost a race or the job was
// already swept.
func (r *Jobs) MarkRunning(ctx context.Context, jobID int64) error {
	const q = `
		UPDATE jobs
		SET status = 'running', started_at = NOW()
		WHERE id = $1 AND status = 'queued'
	`
	tag, err := r.Store.Pool.Exec(ctx, q, jobID)
	if err != nil {
		return fmt.Errorf("jobs mark running: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkDone sets status='done' and finished_at=NOW(). Paired with findings
// inserts; typically the caller does both inside one transaction.
func (r *Jobs) MarkDone(ctx context.Context, jobID int64) error {
	const q = `
		UPDATE jobs
		SET status = 'done', finished_at = NOW(), error = NULL
		WHERE id = $1 AND status IN ('queued', 'running')
	`
	tag, err := r.Store.Pool.Exec(ctx, q, jobID)
	if err != nil {
		return fmt.Errorf("jobs mark done: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkFailed sets status='failed' with a stable error *code* (never raw LLM
// text). Codes are short snake_case strings like "llm_timeout", "llm_parse",
// "db_write", "payload_lost".
func (r *Jobs) MarkFailed(ctx context.Context, jobID int64, code string) error {
	const q = `
		UPDATE jobs
		SET status = 'failed', finished_at = NOW(), error = $2
		WHERE id = $1 AND status IN ('queued', 'running')
	`
	tag, err := r.Store.Pool.Exec(ctx, q, jobID, code)
	if err != nil {
		return fmt.Errorf("jobs mark failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SweepQueuedOlderThan marks any queued jobs older than `age` as failed with
// code='payload_lost'. Called on boot: if the server restarted between POST
// enqueue and the worker receiving the in-memory payload, the row is stuck
// queued forever since the payload is gone (zero-retention).
func (r *Jobs) SweepQueuedOlderThan(ctx context.Context, age time.Duration) (int64, error) {
	const q = `
		UPDATE jobs
		SET status = 'failed', finished_at = NOW(), error = 'payload_lost'
		WHERE status = 'queued' AND created_at < NOW() - $1::interval
	`
	tag, err := r.Store.Pool.Exec(ctx, q, age.String())
	if err != nil {
		return 0, fmt.Errorf("jobs sweep: %w", err)
	}
	return tag.RowsAffected(), nil
}
