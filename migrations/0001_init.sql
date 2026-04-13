-- +goose Up
-- EthicGuard-API initial schema.
-- Zero-retention rule: this schema stores NO Jira issue content (titles, bodies,
-- descriptions, AC text, comments). Only ids, scores, anchors, and refs.

CREATE TABLE installations (
    id              BIGSERIAL PRIMARY KEY,
    cloud_id        TEXT NOT NULL UNIQUE,
    shared_secret   TEXT NOT NULL,
    installed_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE projects (
    id                BIGSERIAL PRIMARY KEY,
    installation_id   BIGINT NOT NULL REFERENCES installations(id) ON DELETE CASCADE,
    project_key       TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (installation_id, project_key)
);

CREATE TABLE policies (
    id            BIGSERIAL PRIMARY KEY,
    scope         TEXT NOT NULL CHECK (scope IN ('company', 'project')),
    owner_id      BIGINT NOT NULL,
    name          TEXT NOT NULL,
    body          JSONB NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE jobs (
    id                        BIGSERIAL PRIMARY KEY,
    installation_id           BIGINT NOT NULL REFERENCES installations(id) ON DELETE CASCADE,
    project_id                BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    issue_key                 TEXT NOT NULL,
    kind                      TEXT NOT NULL,
    status                    TEXT NOT NULL CHECK (status IN ('queued','running','done','failed')),
    error                     TEXT,
    requested_by_account_id   TEXT,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at                TIMESTAMPTZ,
    finished_at               TIMESTAMPTZ
);
CREATE INDEX jobs_status_created_at_idx ON jobs (status, created_at);
CREATE INDEX jobs_installation_issue_idx ON jobs (installation_id, issue_key);

CREATE TABLE findings (
    id            BIGSERIAL PRIMARY KEY,
    job_id        BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    category      TEXT NOT NULL,
    severity      TEXT NOT NULL CHECK (severity IN ('info','low','medium','high')),
    score         SMALLINT NOT NULL,
    anchor        JSONB NOT NULL,
    message_key   TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX findings_job_idx ON findings (job_id);

CREATE TABLE conflicts (
    id            BIGSERIAL PRIMARY KEY,
    job_id        BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    issue_key_a   TEXT NOT NULL,
    issue_key_b   TEXT NOT NULL,
    kind          TEXT NOT NULL,
    severity      TEXT NOT NULL CHECK (severity IN ('info','low','medium','high')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX conflicts_job_idx ON conflicts (job_id);

CREATE TABLE audit_log (
    id                BIGSERIAL PRIMARY KEY,
    installation_id   BIGINT NOT NULL REFERENCES installations(id) ON DELETE CASCADE,
    actor_account_id  TEXT,
    action            TEXT NOT NULL,
    target            TEXT,
    meta              JSONB,
    at                TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX audit_log_installation_at_idx ON audit_log (installation_id, at DESC);

-- +goose Down
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS conflicts;
DROP TABLE IF EXISTS findings;
DROP TABLE IF EXISTS jobs;
DROP TABLE IF EXISTS policies;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS installations;
