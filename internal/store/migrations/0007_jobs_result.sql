-- +goose Up
-- Adds the AC label decision to jobs. Stores one of four enum strings
-- ('AC verified', 'AC defect', 'AC not ready', 'no test') — NULL while
-- queued/running, set when the worker finishes. Zero-retention: this is an
-- enum tag describing the analysis outcome, not issue content.

ALTER TABLE jobs ADD COLUMN result_label TEXT;

CREATE INDEX jobs_installation_id_created_idx
    ON jobs (installation_id, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS jobs_installation_id_created_idx;
ALTER TABLE jobs DROP COLUMN IF EXISTS result_label;
