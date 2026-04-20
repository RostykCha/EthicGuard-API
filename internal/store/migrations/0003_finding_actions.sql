-- +goose Up
-- `finding_actions` records user responses to findings (accept / dismiss)
-- plus their structured reason, per Phase 1 "inline resolve actions."
-- Zero-retention: `action` and `reason` are enum strings, never free text.
-- `actor_account_id` is the Jira account id taken from the Forge JWT.

CREATE TABLE finding_actions (
    id                BIGSERIAL PRIMARY KEY,
    finding_id        BIGINT NOT NULL REFERENCES findings(id) ON DELETE CASCADE,
    action            TEXT NOT NULL CHECK (action IN ('accept', 'dismiss')),
    reason            TEXT CHECK (reason IS NULL OR reason IN (
                        'false_positive', 'wont_fix', 'duplicate', 'noise', 'other'
                      )),
    actor_account_id  TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (finding_id)
);

CREATE INDEX finding_actions_action_idx ON finding_actions (action);

-- +goose Down
DROP INDEX IF EXISTS finding_actions_action_idx;
DROP TABLE IF EXISTS finding_actions;
