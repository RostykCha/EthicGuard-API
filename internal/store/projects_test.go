package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

const projectsUpsertSQL = `
			INSERT INTO projects (installation_id, project_key)
			VALUES ($1, $2)
			ON CONFLICT (installation_id, project_key) DO UPDATE
			SET project_key = EXCLUDED.project_key
			RETURNING id
		`

const projectsGetConfigSQL = `
			SELECT project_key, tested_issue_types,
			       agent_enabled, agent_severity_threshold, agent_prompt_addendum
			FROM projects
			WHERE installation_id = $1 AND project_key = $2
		`

const projectsSetConfigSQL = `
			INSERT INTO projects (
				installation_id, project_key,
				tested_issue_types,
				agent_enabled, agent_severity_threshold, agent_prompt_addendum
			)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (installation_id, project_key) DO UPDATE
			SET tested_issue_types        = EXCLUDED.tested_issue_types,
			    agent_enabled             = EXCLUDED.agent_enabled,
			    agent_severity_threshold  = EXCLUDED.agent_severity_threshold,
			    agent_prompt_addendum     = EXCLUDED.agent_prompt_addendum
			RETURNING project_key, tested_issue_types,
			          agent_enabled, agent_severity_threshold, agent_prompt_addendum
		`

func TestProjects_Upsert(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(projectsUpsertSQL).
		WithArgs(int64(7), "KAN").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(123)))

	r := &Projects{Store: s}
	id, err := r.Upsert(context.Background(), 7, "KAN")
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if id != 123 {
		t.Errorf("id = %d, want 123", id)
	}
}

func TestProjects_Upsert_Error(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(projectsUpsertSQL).
		WithArgs(int64(1), "K").
		WillReturnError(errors.New("boom"))

	r := &Projects{Store: s}
	if _, err := r.Upsert(context.Background(), 1, "K"); err == nil {
		t.Fatal("expected error")
	}
}

func TestProjects_GetConfig_Found(t *testing.T) {
	s, mock := newMockStore(t)
	rows := pgxmock.NewRows([]string{
		"project_key", "tested_issue_types",
		"agent_enabled", "agent_severity_threshold", "agent_prompt_addendum",
	}).AddRow("KAN", []string{"10001", "10002"}, true, "medium", "be strict")

	mock.ExpectQuery(projectsGetConfigSQL).
		WithArgs(int64(7), "KAN").
		WillReturnRows(rows)

	r := &Projects{Store: s}
	cfg, err := r.GetConfig(context.Background(), 7, "KAN")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	want := &ProjectConfig{
		ProjectKey:             "KAN",
		TestedIssueTypes:       []string{"10001", "10002"},
		AgentEnabled:           true,
		AgentSeverityThreshold: "medium",
		AgentPromptAddendum:    "be strict",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got %+v, want %+v", cfg, want)
	}
}

func TestProjects_GetConfig_NotFound(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(projectsGetConfigSQL).
		WithArgs(int64(7), "KAN").
		WillReturnError(pgx.ErrNoRows)

	r := &Projects{Store: s}
	_, err := r.GetConfig(context.Background(), 7, "KAN")
	if !IsNotFound(err) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestProjects_GetConfig_OtherError(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(projectsGetConfigSQL).
		WithArgs(int64(7), "KAN").
		WillReturnError(errors.New("db blew up"))

	r := &Projects{Store: s}
	_, err := r.GetConfig(context.Background(), 7, "KAN")
	if err == nil || IsNotFound(err) {
		t.Errorf("expected non-NotFound error, got %v", err)
	}
}

func TestProjects_SetConfig(t *testing.T) {
	s, mock := newMockStore(t)
	rows := pgxmock.NewRows([]string{
		"project_key", "tested_issue_types",
		"agent_enabled", "agent_severity_threshold", "agent_prompt_addendum",
	}).AddRow("KAN", []string{"10001"}, false, "high", "")

	mock.ExpectQuery(projectsSetConfigSQL).
		WithArgs(int64(7), "KAN", []string{"10001"}, false, "high", "").
		WillReturnRows(rows)

	r := &Projects{Store: s}
	cfg, err := r.SetConfig(context.Background(), 7, "KAN", ProjectConfig{
		ProjectKey:             "KAN",
		TestedIssueTypes:       []string{"10001"},
		AgentEnabled:           false,
		AgentSeverityThreshold: "high",
	})
	if err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if cfg.ProjectKey != "KAN" || !reflect.DeepEqual(cfg.TestedIssueTypes, []string{"10001"}) {
		t.Errorf("got %+v", cfg)
	}
}

func TestProjects_SetConfig_NilTypesNormalizedToEmpty(t *testing.T) {
	s, mock := newMockStore(t)
	rows := pgxmock.NewRows([]string{
		"project_key", "tested_issue_types",
		"agent_enabled", "agent_severity_threshold", "agent_prompt_addendum",
	}).AddRow("KAN", []string{}, false, "info", "")

	// nil TestedIssueTypes must be normalized to []string{} before reaching pgx —
	// otherwise pgx encodes a NULL where the schema expects a TEXT[].
	mock.ExpectQuery(projectsSetConfigSQL).
		WithArgs(int64(7), "KAN", []string{}, false, "info", "").
		WillReturnRows(rows)

	r := &Projects{Store: s}
	if _, err := r.SetConfig(context.Background(), 7, "KAN", ProjectConfig{
		ProjectKey:             "KAN",
		AgentSeverityThreshold: "info",
	}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
}

func TestProjects_SetConfig_Error(t *testing.T) {
	s, mock := newMockStore(t)
	mock.ExpectQuery(projectsSetConfigSQL).
		WithArgs(int64(7), "KAN", []string{}, false, "low", "").
		WillReturnError(errors.New("constraint violation"))

	r := &Projects{Store: s}
	_, err := r.SetConfig(context.Background(), 7, "KAN", ProjectConfig{
		ProjectKey:             "KAN",
		AgentSeverityThreshold: "low",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}
