-- +goose Up
-- Per-project agent configuration: enable/disable the EthicGuard AC Reviewer
-- agent, set a minimum severity threshold below which findings are dropped
-- before the AC label is decided, and an optional prompt addendum the worker
-- appends to the analysis system prompt for this project.
--
-- Zero-retention: all three columns are configuration metadata, not issue
-- content. The addendum is admin-authored guidance ("focus on accessibility
-- criteria", etc.) and is explicitly NOT a place to paste Jira content; the
-- UI surfaces this contract to the admin and the handler bounds the length.

ALTER TABLE projects
    ADD COLUMN agent_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN agent_severity_threshold TEXT NOT NULL DEFAULT 'medium'
        CHECK (agent_severity_threshold IN ('info', 'low', 'medium', 'high')),
    ADD COLUMN agent_prompt_addendum TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE projects DROP COLUMN IF EXISTS agent_prompt_addendum;
ALTER TABLE projects DROP COLUMN IF EXISTS agent_severity_threshold;
ALTER TABLE projects DROP COLUMN IF EXISTS agent_enabled;
