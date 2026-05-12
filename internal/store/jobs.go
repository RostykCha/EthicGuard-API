package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// JobStatus is the small enum stored in jobs.status.
type JobStatus string

const (
	JobQueued  JobStatus = "queued"
	JobRunning JobStatus = "running"
	JobDone    JobStatus = "done"
	JobFailed  JobStatus = "failed"
)

// Job is the persisted record for a single analysis run. Zero-retention: no
// issue content lands here — only ids, kind, status, an error code, and the
// label decision once finished.
type Job struct {
	ID                    int64
	InstallationID        int64
	ProjectID             int64
	IssueKey              string
	Kind                  string
	Status                JobStatus
	Error                 string
	RequestedByAccountID  string
	ResultLabel           string
	CreatedAt             time.Time
	StartedAt             *time.Time
	FinishedAt            *time.Time
}

// Jobs repository over the `jobs` table.
type Jobs struct {
	Store *Store
}

// Enqueue inserts a new queued job and returns its id. Caller must have
// already resolved the project id via Projects.Upsert.
func (r *Jobs) Enqueue(ctx context.Context, installationID, projectID int64, issueKey, kind, requestedBy string) (int64, error) {
	const q = `
		INSERT INTO jobs (installation_id, project_id, issue_key, kind, status, requested_by_account_id)
		VALUES ($1, $2, $3, $4, 'queued', NULLIF($5, ''))
		RETURNING id
	`
	var id int64
	if err := r.Store.DB.QueryRow(ctx, q, installationID, projectID, issueKey, kind, requestedBy).Scan(&id); err != nil {
		return 0, fmt.Errorf("jobs enqueue: %w", err)
	}
	return id, nil
}

// ClaimNext atomically picks the oldest queued job, marks it as running, and
// returns it. Returns ErrNotFound when nothing is queued. Uses FOR UPDATE SKIP
// LOCKED so multiple workers can race safely.
func (r *Jobs) ClaimNext(ctx context.Context) (*Job, error) {
	const q = `
		UPDATE jobs
		SET status = 'running', started_at = NOW()
		WHERE id = (
			SELECT id FROM jobs
			WHERE status = 'queued'
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, installation_id, project_id, issue_key, kind, status,
		          COALESCE(error, ''), COALESCE(requested_by_account_id, ''),
		          COALESCE(result_label, ''), created_at, started_at, finished_at
	`
	row := r.Store.DB.QueryRow(ctx, q)
	job := &Job{}
	if err := scanJob(row, job); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("jobs claim: %w", err)
	}
	return job, nil
}

// MarkDone sets status='done', records the label decision, and stamps
// finished_at. Findings are written separately via the Findings repo.
func (r *Jobs) MarkDone(ctx context.Context, jobID int64, resultLabel string) error {
	const q = `
		UPDATE jobs
		SET status = 'done', finished_at = NOW(), result_label = $2, error = NULL
		WHERE id = $1
	`
	if _, err := r.Store.DB.Exec(ctx, q, jobID, resultLabel); err != nil {
		return fmt.Errorf("jobs mark done: %w", err)
	}
	return nil
}

// MarkFailed sets status='failed' and stores a stable error CODE (never an
// LLM-raw error message) so we don't leak issue content into Postgres.
func (r *Jobs) MarkFailed(ctx context.Context, jobID int64, errCode string) error {
	const q = `
		UPDATE jobs
		SET status = 'failed', finished_at = NOW(), error = $2
		WHERE id = $1
	`
	if _, err := r.Store.DB.Exec(ctx, q, jobID, errCode); err != nil {
		return fmt.Errorf("jobs mark failed: %w", err)
	}
	return nil
}

// GetByID returns the job — scoped to an installation so a request can't
// peek into another tenant's jobs.
func (r *Jobs) GetByID(ctx context.Context, installationID, jobID int64) (*Job, error) {
	const q = `
		SELECT id, installation_id, project_id, issue_key, kind, status,
		       COALESCE(error, ''), COALESCE(requested_by_account_id, ''),
		       COALESCE(result_label, ''), created_at, started_at, finished_at
		FROM jobs
		WHERE id = $1 AND installation_id = $2
	`
	row := r.Store.DB.QueryRow(ctx, q, jobID, installationID)
	job := &Job{}
	if err := scanJob(row, job); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("jobs get: %w", err)
	}
	return job, nil
}

// LatestForIssue returns the most recent job for an issue under an
// installation, or ErrNotFound when the issue has never been analyzed.
func (r *Jobs) LatestForIssue(ctx context.Context, installationID int64, issueKey string) (*Job, error) {
	const q = `
		SELECT id, installation_id, project_id, issue_key, kind, status,
		       COALESCE(error, ''), COALESCE(requested_by_account_id, ''),
		       COALESCE(result_label, ''), created_at, started_at, finished_at
		FROM jobs
		WHERE installation_id = $1 AND issue_key = $2
		ORDER BY created_at DESC
		LIMIT 1
	`
	row := r.Store.DB.QueryRow(ctx, q, installationID, issueKey)
	job := &Job{}
	if err := scanJob(row, job); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("jobs latest: %w", err)
	}
	return job, nil
}

func scanJob(row pgx.Row, j *Job) error {
	var status string
	if err := row.Scan(
		&j.ID, &j.InstallationID, &j.ProjectID, &j.IssueKey, &j.Kind, &status,
		&j.Error, &j.RequestedByAccountID, &j.ResultLabel,
		&j.CreatedAt, &j.StartedAt, &j.FinishedAt,
	); err != nil {
		return err
	}
	j.Status = JobStatus(status)
	return nil
}
