package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

const jobsEnqueueSQL = `
			INSERT INTO jobs (installation_id, project_id, issue_key, kind, status, requested_by_account_id)
			VALUES ($1, $2, $3, $4, 'queued', NULLIF($5, ''))
			RETURNING id
		`

const jobsClaimNextSQL = `
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

const jobsRecordCompletedSQL = `
		INSERT INTO jobs (installation_id, project_id, issue_key, kind, status,
		                  result_label, started_at, finished_at)
		VALUES ($1, $2, $3, $4, 'done', $5, NOW(), NOW())
		RETURNING id
	`

const jobsMarkDoneSQL = `
			UPDATE jobs
			SET status = 'done', finished_at = NOW(), result_label = $2, error = NULL
			WHERE id = $1
		`

const jobsMarkFailedSQL = `
			UPDATE jobs
			SET status = 'failed', finished_at = NOW(), error = $2
			WHERE id = $1
		`

const jobsGetByIDSQL = `
			SELECT id, installation_id, project_id, issue_key, kind, status,
			       COALESCE(error, ''), COALESCE(requested_by_account_id, ''),
			       COALESCE(result_label, ''), created_at, started_at, finished_at
			FROM jobs
			WHERE id = $1 AND installation_id = $2
		`

const jobsCountCoveredIssuesSQL = `
			SELECT COUNT(DISTINCT j.issue_key)
			FROM jobs j
			JOIN projects p ON p.id = j.project_id
			WHERE j.installation_id = $1
			  AND p.project_key = $2
			  AND j.status = 'done'
		`

const jobsLatestForIssueSQL = `
			SELECT id, installation_id, project_id, issue_key, kind, status,
			       COALESCE(error, ''), COALESCE(requested_by_account_id, ''),
			       COALESCE(result_label, ''), created_at, started_at, finished_at
			FROM jobs
			WHERE installation_id = $1 AND issue_key = $2
			ORDER BY created_at DESC
			LIMIT 1
		`

func jobColumns() []string {
	return []string{
		"id", "installation_id", "project_id", "issue_key", "kind", "status",
		"error", "requested_by_account_id", "result_label",
		"created_at", "started_at", "finished_at",
	}
}

func jobRow(id, instID, projID int64, issueKey, kind, status, errCode, by, label string,
	createdAt time.Time, startedAt, finishedAt *time.Time,
) *pgxmock.Rows {
	return pgxmock.NewRows(jobColumns()).
		AddRow(id, instID, projID, issueKey, kind, status, errCode, by, label,
			createdAt, startedAt, finishedAt)
}

func TestJobs_Enqueue(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(jobsEnqueueSQL).
		WithArgs(int64(1), int64(2), "KAN-1", "ac_check", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(99)))

	r := &Jobs{Store: s}
	id, err := r.Enqueue(context.Background(), 1, 2, "KAN-1", "ac_check", "user-1")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if id != 99 {
		t.Errorf("id = %d, want 99", id)
	}
}

func TestJobs_Enqueue_Error(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(jobsEnqueueSQL).
		WithArgs(int64(1), int64(2), "K-1", "k", "").
		WillReturnError(errors.New("boom"))

	r := &Jobs{Store: s}
	if _, err := r.Enqueue(context.Background(), 1, 2, "K-1", "k", ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestJobs_ClaimNext_Found(t *testing.T) {
	s, mock := newMockStore(t)
	now := time.Now()
	mock.ExpectQuery(jobsClaimNextSQL).
		WillReturnRows(jobRow(1, 2, 3, "KAN-1", "ac_check", "running", "", "u", "",
			now, &now, nil))

	r := &Jobs{Store: s}
	got, err := r.ClaimNext(context.Background())
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if got.ID != 1 || got.Status != JobRunning || got.IssueKey != "KAN-1" {
		t.Errorf("got %+v", got)
	}
}

func TestJobs_ClaimNext_NoneQueued(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(jobsClaimNextSQL).WillReturnError(pgx.ErrNoRows)

	r := &Jobs{Store: s}
	_, err := r.ClaimNext(context.Background())
	if !IsNotFound(err) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestJobs_ClaimNext_OtherError(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(jobsClaimNextSQL).WillReturnError(errors.New("db down"))

	r := &Jobs{Store: s}
	_, err := r.ClaimNext(context.Background())
	if err == nil || IsNotFound(err) {
		t.Errorf("expected non-NotFound error, got %v", err)
	}
}

func TestJobs_MarkDone(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectExec(jobsMarkDoneSQL).
		WithArgs(int64(7), "AC-verified").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	r := &Jobs{Store: s}
	if err := r.MarkDone(context.Background(), 7, "AC-verified"); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}
}

func TestJobs_MarkDone_Error(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectExec(jobsMarkDoneSQL).
		WithArgs(int64(7), "AC-defect").
		WillReturnError(errors.New("boom"))

	r := &Jobs{Store: s}
	if err := r.MarkDone(context.Background(), 7, "AC-defect"); err == nil {
		t.Fatal("expected error")
	}
}

func TestJobs_MarkFailed(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectExec(jobsMarkFailedSQL).
		WithArgs(int64(7), "llm_timeout").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	r := &Jobs{Store: s}
	if err := r.MarkFailed(context.Background(), 7, "llm_timeout"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
}

func TestJobs_MarkFailed_Error(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectExec(jobsMarkFailedSQL).
		WithArgs(int64(7), "x").
		WillReturnError(errors.New("boom"))

	r := &Jobs{Store: s}
	if err := r.MarkFailed(context.Background(), 7, "x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestJobs_GetByID_Found(t *testing.T) {
	s, mock := newMockStore(t)
	now := time.Now()
	mock.ExpectQuery(jobsGetByIDSQL).
		WithArgs(int64(7), int64(2)).
		WillReturnRows(jobRow(7, 2, 3, "KAN-1", "k", "done", "", "", "AC-verified",
			now, &now, &now))

	r := &Jobs{Store: s}
	got, err := r.GetByID(context.Background(), 2, 7)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != 7 || got.Status != JobDone || got.ResultLabel != "AC-verified" {
		t.Errorf("got %+v", got)
	}
}

func TestJobs_GetByID_NotFound(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(jobsGetByIDSQL).
		WithArgs(int64(7), int64(2)).
		WillReturnError(pgx.ErrNoRows)

	r := &Jobs{Store: s}
	_, err := r.GetByID(context.Background(), 2, 7)
	if !IsNotFound(err) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestJobs_LatestForIssue_Found(t *testing.T) {
	s, mock := newMockStore(t)
	now := time.Now()
	mock.ExpectQuery(jobsLatestForIssueSQL).
		WithArgs(int64(2), "KAN-1").
		WillReturnRows(jobRow(11, 2, 3, "KAN-1", "k", "done", "", "", "AC-verified",
			now, &now, &now))

	r := &Jobs{Store: s}
	got, err := r.LatestForIssue(context.Background(), 2, "KAN-1")
	if err != nil {
		t.Fatalf("LatestForIssue: %v", err)
	}
	if got.ID != 11 {
		t.Errorf("got %+v", got)
	}
}

func TestJobs_LatestForIssue_NotFound(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(jobsLatestForIssueSQL).
		WithArgs(int64(2), "KAN-1").
		WillReturnError(pgx.ErrNoRows)

	r := &Jobs{Store: s}
	_, err := r.LatestForIssue(context.Background(), 2, "KAN-1")
	if !IsNotFound(err) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestJobs_CountCoveredIssues(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(jobsCountCoveredIssuesSQL).
		WithArgs(int64(42), "KAN").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(3)))

	r := &Jobs{Store: s}
	n, err := r.CountCoveredIssues(context.Background(), 42, "KAN")
	if err != nil {
		t.Fatalf("CountCoveredIssues: %v", err)
	}
	if n != 3 {
		t.Errorf("n = %d, want 3", n)
	}
}

func TestJobs_CountCoveredIssues_Zero(t *testing.T) {
	// A project that has never been analyzed (or doesn't yet have a projects
	// row) returns 0, not an error. The metric is defined on the
	// (installation, project_key) pair regardless of join membership.
	s, mock := newMockStore(t)
	mock.ExpectQuery(jobsCountCoveredIssuesSQL).
		WithArgs(int64(42), "EMPTY").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(0)))

	r := &Jobs{Store: s}
	n, err := r.CountCoveredIssues(context.Background(), 42, "EMPTY")
	if err != nil {
		t.Fatalf("CountCoveredIssues: %v", err)
	}
	if n != 0 {
		t.Errorf("n = %d, want 0", n)
	}
}

func TestJobs_CountCoveredIssues_Error(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(jobsCountCoveredIssuesSQL).
		WithArgs(int64(42), "KAN").
		WillReturnError(errors.New("db down"))

	r := &Jobs{Store: s}
	if _, err := r.CountCoveredIssues(context.Background(), 42, "KAN"); err == nil {
		t.Fatal("expected error")
	}
}

func TestJobs_RecordCompleted(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(jobsRecordCompletedSQL).
		WithArgs(int64(1), int64(2), "KAN-1", "ac_quality", "AC-verified").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(77)))

	r := &Jobs{Store: s}
	id, err := r.RecordCompleted(context.Background(), 1, 2, "KAN-1", "ac_quality", "AC-verified")
	if err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}
	if id != 77 {
		t.Errorf("id = %d, want 77", id)
	}
}

func TestJobs_RecordCompleted_Error(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(jobsRecordCompletedSQL).
		WithArgs(int64(1), int64(2), "KAN-1", "ac_quality", "AC-defect").
		WillReturnError(errors.New("boom"))

	r := &Jobs{Store: s}
	if _, err := r.RecordCompleted(context.Background(), 1, 2, "KAN-1", "ac_quality", "AC-defect"); err == nil {
		t.Fatal("expected error")
	}
}
