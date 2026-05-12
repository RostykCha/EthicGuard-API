package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

const installationsUpsertSQL = `
			INSERT INTO installations (cloud_id, shared_secret, installed_at, updated_at)
			VALUES ($1, $2, NOW(), NOW())
			ON CONFLICT (cloud_id) DO UPDATE
			SET shared_secret = EXCLUDED.shared_secret,
			    updated_at    = NOW()
			RETURNING id, cloud_id, shared_secret
		`

const installationsGetByCloudIDSQL = `SELECT id, cloud_id, shared_secret FROM installations WHERE cloud_id = $1`

const installationsDeleteByCloudIDSQL = `DELETE FROM installations WHERE cloud_id = $1`

func TestInstallations_Upsert(t *testing.T) {
	s, mock := newMockStore(t)
	rows := pgxmock.NewRows([]string{"id", "cloud_id", "shared_secret"}).
		AddRow(int64(42), "cloud-abc", "secret-xyz")
	mock.ExpectQuery(installationsUpsertSQL).
		WithArgs("cloud-abc", "secret-xyz").
		WillReturnRows(rows)

	r := &Installations{Store: s}
	got, err := r.Upsert(context.Background(), "cloud-abc", "secret-xyz")
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got.ID != 42 || got.CloudID != "cloud-abc" || got.SharedSecret != "secret-xyz" {
		t.Errorf("got %+v, want {42 cloud-abc secret-xyz}", got)
	}
}

func TestInstallations_Upsert_ScanError(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(installationsUpsertSQL).
		WithArgs("c", "s").
		WillReturnError(errors.New("db down"))

	r := &Installations{Store: s}
	if _, err := r.Upsert(context.Background(), "c", "s"); err == nil {
		t.Fatal("expected error")
	}
}

func TestInstallations_GetByCloudID_Found(t *testing.T) {
	s, mock := newMockStore(t)
	rows := pgxmock.NewRows([]string{"id", "cloud_id", "shared_secret"}).
		AddRow(int64(1), "c", "s")
	mock.ExpectQuery(installationsGetByCloudIDSQL).
		WithArgs("c").
		WillReturnRows(rows)

	r := &Installations{Store: s}
	got, err := r.GetByCloudID(context.Background(), "c")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != 1 {
		t.Errorf("ID = %d, want 1", got.ID)
	}
}

func TestInstallations_GetByCloudID_NotFound(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(installationsGetByCloudIDSQL).
		WithArgs("missing").
		WillReturnError(pgx.ErrNoRows)

	r := &Installations{Store: s}
	_, err := r.GetByCloudID(context.Background(), "missing")
	if !IsNotFound(err) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestInstallations_GetByCloudID_OtherError(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(installationsGetByCloudIDSQL).
		WithArgs("c").
		WillReturnError(errors.New("boom"))

	r := &Installations{Store: s}
	_, err := r.GetByCloudID(context.Background(), "c")
	if err == nil || IsNotFound(err) {
		t.Errorf("expected non-NotFound error, got %v", err)
	}
}

func TestInstallations_DeleteByCloudID_Found(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectExec(installationsDeleteByCloudIDSQL).
		WithArgs("c").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	r := &Installations{Store: s}
	if err := r.DeleteByCloudID(context.Background(), "c"); err != nil {
		t.Errorf("Delete: %v", err)
	}
}

func TestInstallations_DeleteByCloudID_NotFound(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectExec(installationsDeleteByCloudIDSQL).
		WithArgs("c").
		WillReturnResult(pgxmock.NewResult("DELETE", 0))

	r := &Installations{Store: s}
	err := r.DeleteByCloudID(context.Background(), "c")
	if !IsNotFound(err) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestInstallations_DeleteByCloudID_ExecError(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectExec(installationsDeleteByCloudIDSQL).
		WithArgs("c").
		WillReturnError(errors.New("boom"))

	r := &Installations{Store: s}
	err := r.DeleteByCloudID(context.Background(), "c")
	if err == nil || IsNotFound(err) {
		t.Errorf("expected wrapped error, got %v", err)
	}
}
