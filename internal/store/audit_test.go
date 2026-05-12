package store

import (
	"context"
	"errors"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// SQL pinned to the production string so a copy-paste drift in audit.go
// fails this test.
const auditInsertSQL = `
			INSERT INTO audit_log (installation_id, actor_account_id, action, target, meta)
			VALUES ($1, NULLIF($2, ''), $3, NULLIF($4, ''), $5)
		`

func TestAudits_Log_WithMeta(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectExec(auditInsertSQL).
		WithArgs(int64(7), "u-1", "project.config.update", "KAN", []byte(`{"k":"v"}`)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	r := &Audits{Store: s}
	err := r.Log(context.Background(), 7, "u-1", "project.config.update", "KAN",
		map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
}

func TestAudits_Log_NilMeta(t *testing.T) {
	s, mock := newMockStore(t)
	// nil meta passes a nil []byte through pgx — assert that explicitly.
	mock.ExpectExec(auditInsertSQL).
		WithArgs(int64(1), "", "install.completed", "", []byte(nil)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	r := &Audits{Store: s}
	if err := r.Log(context.Background(), 1, "", "install.completed", "", nil); err != nil {
		t.Fatalf("Log: %v", err)
	}
}

func TestAudits_Log_ExecError(t *testing.T) {
	s, mock := newMockStore(t)
	boom := errors.New("connection closed")
	mock.ExpectExec(auditInsertSQL).
		WithArgs(int64(1), "u", "a", "t", []byte(nil)).
		WillReturnError(boom)

	r := &Audits{Store: s}
	err := r.Log(context.Background(), 1, "u", "a", "t", nil)
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected wrapped boom, got %v", err)
	}
}

func TestAudits_Log_BadMetaReturnsError(t *testing.T) {
	// Meta containing a channel is not JSON-encodable; constructor must
	// surface that as an error instead of panicking. No SQL is executed.
	s, _ := newMockStore(t)
	r := &Audits{Store: s}
	err := r.Log(context.Background(), 1, "u", "a", "t",
		map[string]any{"ch": make(chan int)})
	if err == nil {
		t.Fatal("expected marshal error")
	}
}
