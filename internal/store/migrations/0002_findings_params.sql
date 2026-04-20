-- +goose Up
-- Add `params` JSONB to `findings`. Zero-retention note: params carries only
-- non-content metadata the catalog needs to render a finding's human text
-- (field names, counts, enum values — never user-authored text like quoted
-- description snippets). The resolver in internal/catalog validates each
-- param value against a whitelist pattern; anything else fails the job.
--
-- No schema change for `anchor` — it stays JSONB, so extending the shape
-- from {field} to {field, start, end} is a code-only change.

ALTER TABLE findings
    ADD COLUMN params JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Index to help the worker "payload lost" janitor sweep stale queued jobs.
CREATE INDEX IF NOT EXISTS jobs_queued_created_at_idx
    ON jobs (created_at) WHERE status = 'queued';

-- +goose Down
DROP INDEX IF EXISTS jobs_queued_created_at_idx;
ALTER TABLE findings DROP COLUMN IF EXISTS params;
