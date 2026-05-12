package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

const findingsInsertSQL = `
			INSERT INTO findings (job_id, category, severity, score, anchor, message_key)
			SELECT $1, c, s, sc, a::jsonb, m
			FROM UNNEST($2::TEXT[], $3::TEXT[], $4::SMALLINT[], $5::TEXT[], $6::TEXT[])
			     AS u(c, s, sc, a, m)
		`

const findingsListByJobSQL = `
			SELECT id, job_id, category, severity, score, anchor, message_key, created_at
			FROM findings
			WHERE job_id = $1
			ORDER BY id
		`

func TestFindings_InsertBatch_Empty(t *testing.T) {
	// Empty input must be a no-op — zero SQL roundtrips. Strict expectations
	// on the mock would catch any accidental call.
	s, _ := newMockStore(t)
	r := &Findings{Store: s}
	if err := r.InsertBatch(context.Background(), 1, nil); err != nil {
		t.Errorf("nil findings: %v", err)
	}
	if err := r.InsertBatch(context.Background(), 1, []PersistedFinding{}); err != nil {
		t.Errorf("empty findings: %v", err)
	}
}

func TestFindings_InsertBatch_Multi(t *testing.T) {
	s, mock := newMockStore(t)
	findings := []PersistedFinding{
		{Category: "ambiguity", Severity: "medium", Score: 60,
			Anchor: map[string]any{"field": "ac", "start": float64(0), "end": float64(5)},
			MessageKey: "ambiguity.vague"},
		{Category: "missing_negative", Severity: "high", Score: 80,
			Anchor: map[string]any{"field": "ac"},
			MessageKey: "missing.negative_case"},
	}
	mock.ExpectExec(findingsInsertSQL).
		WithArgs(
			int64(42),
			[]string{"ambiguity", "missing_negative"},
			[]string{"medium", "high"},
			[]int32{60, 80},
			[][]byte{
				[]byte(`{"end":5,"field":"ac","start":0}`),
				[]byte(`{"field":"ac"}`),
			},
			[]string{"ambiguity.vague", "missing.negative_case"},
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 2))

	r := &Findings{Store: s}
	if err := r.InsertBatch(context.Background(), 42, findings); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}
}

func TestFindings_InsertBatch_ExecError(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectExec(findingsInsertSQL).
		WithArgs(
			int64(42),
			[]string{"c"},
			[]string{"low"},
			[]int32{10},
			[][]byte{[]byte(`null`)},
			[]string{"k"},
		).
		WillReturnError(errors.New("boom"))

	r := &Findings{Store: s}
	err := r.InsertBatch(context.Background(), 42, []PersistedFinding{
		{Category: "c", Severity: "low", Score: 10, MessageKey: "k"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestFindings_ListByJob(t *testing.T) {
	s, mock := newMockStore(t)
	created := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	rows := pgxmock.NewRows([]string{
		"id", "job_id", "category", "severity", "score",
		"anchor", "message_key", "created_at",
	}).
		AddRow(int64(1), int64(42), "ambiguity", "medium", int32(60),
			[]byte(`{"field":"ac"}`), "ambiguity.vague", created).
		AddRow(int64(2), int64(42), "missing", "high", int32(80),
			[]byte(``), "missing.negative_case", created)

	mock.ExpectQuery(findingsListByJobSQL).
		WithArgs(int64(42)).
		WillReturnRows(rows)

	r := &Findings{Store: s}
	got, err := r.ListByJob(context.Background(), 42)
	if err != nil {
		t.Fatalf("ListByJob: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if !reflect.DeepEqual(got[0].Anchor, map[string]any{"field": "ac"}) {
		t.Errorf("row 0 anchor = %+v", got[0].Anchor)
	}
	// Row 1 has an empty anchor blob — Anchor must stay nil, not crash.
	if got[1].Anchor != nil {
		t.Errorf("row 1 anchor should be nil, got %+v", got[1].Anchor)
	}
	if got[0].Score != 60 || got[1].Score != 80 {
		t.Errorf("scores = %d, %d", got[0].Score, got[1].Score)
	}
}

func TestFindings_ListByJob_QueryError(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(findingsListByJobSQL).
		WithArgs(int64(42)).
		WillReturnError(errors.New("boom"))

	r := &Findings{Store: s}
	if _, err := r.ListByJob(context.Background(), 42); err == nil {
		t.Fatal("expected error")
	}
}

func TestFindings_ListByJob_ScanError(t *testing.T) {
	s, mock := newMockStore(t)
	// Return a row with the wrong column types so Scan fails.
	rows := pgxmock.NewRows([]string{
		"id", "job_id", "category", "severity", "score",
		"anchor", "message_key", "created_at",
	}).AddRow("not-an-int", int64(1), "c", "s", int32(1), []byte(`{}`), "k", time.Now())

	mock.ExpectQuery(findingsListByJobSQL).
		WithArgs(int64(42)).
		WillReturnRows(rows)

	r := &Findings{Store: s}
	if _, err := r.ListByJob(context.Background(), 42); err == nil {
		t.Fatal("expected scan error")
	}
}

func TestFindings_ListByJob_BadAnchorJSON(t *testing.T) {
	s, mock := newMockStore(t)
	rows := pgxmock.NewRows([]string{
		"id", "job_id", "category", "severity", "score",
		"anchor", "message_key", "created_at",
	}).AddRow(int64(1), int64(42), "c", "s", int32(1),
		[]byte(`{not json`), "k", time.Now())

	mock.ExpectQuery(findingsListByJobSQL).
		WithArgs(int64(42)).
		WillReturnRows(rows)

	r := &Findings{Store: s}
	if _, err := r.ListByJob(context.Background(), 42); err == nil {
		t.Fatal("expected unmarshal error")
	}
}
