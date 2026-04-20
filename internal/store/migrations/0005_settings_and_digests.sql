-- +goose Up
-- Phase 2 #6 + Phase 3 #10 + #11 schema.
--
-- Zero-retention: none of these columns/tables carry free text. Thresholds
-- and overrides are numbers, persona is an enum, digest rows reference
-- findings by id. The UI re-resolves human text via the catalog on render.

-- Phase 2 #6: per-project confidence floor + per-category overrides learned
-- from dismissals (#7 writes to threshold_overrides).
ALTER TABLE projects
    ADD COLUMN confidence_threshold INTEGER NOT NULL DEFAULT 0
        CHECK (confidence_threshold >= 0 AND confidence_threshold <= 100);
ALTER TABLE projects
    ADD COLUMN threshold_overrides JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Phase 3 #10: per-user role override.
CREATE TABLE user_preferences (
    id               BIGSERIAL PRIMARY KEY,
    installation_id  BIGINT NOT NULL REFERENCES installations(id) ON DELETE CASCADE,
    account_id       TEXT NOT NULL,
    persona          TEXT CHECK (persona IN ('pm', 'qa', 'dev')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (installation_id, account_id)
);
CREATE INDEX user_preferences_installation_idx
    ON user_preferences (installation_id);

-- Phase 3 #11: weekly cross-issue digest snapshots.
-- finding_ids is an array of int64 pointers into the findings table; human
-- text is not stored here. When findings cascade-delete (installation
-- uninstall), the digest row still dangles harmlessly — but cascade from
-- installations → digests removes them first.
CREATE TABLE digests (
    id               BIGSERIAL PRIMARY KEY,
    installation_id  BIGINT NOT NULL REFERENCES installations(id) ON DELETE CASCADE,
    period_start     TIMESTAMPTZ NOT NULL,
    period_end       TIMESTAMPTZ NOT NULL,
    finding_ids      BIGINT[] NOT NULL DEFAULT ARRAY[]::BIGINT[],
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX digests_installation_created_idx
    ON digests (installation_id, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS digests_installation_created_idx;
DROP TABLE IF EXISTS digests;
DROP INDEX IF EXISTS user_preferences_installation_idx;
DROP TABLE IF EXISTS user_preferences;
ALTER TABLE projects DROP COLUMN IF EXISTS threshold_overrides;
ALTER TABLE projects DROP COLUMN IF EXISTS confidence_threshold;
