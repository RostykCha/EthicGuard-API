-- +goose Up
-- Adds the per-project list of Jira issue-type IDs that are in scope for QA
-- analysis. This is installation-scoped configuration metadata, not issue
-- content — no free text is stored. Empty array means "not yet configured,"
-- the API treats that as "don't analyze anything until the admin picks types"
-- in future handlers, but existing endpoints remain unaffected by the default.

ALTER TABLE projects
    ADD COLUMN tested_issue_types TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[];

-- +goose Down
ALTER TABLE projects DROP COLUMN IF EXISTS tested_issue_types;
