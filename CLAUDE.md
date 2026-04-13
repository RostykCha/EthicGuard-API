# EthicGuard-API — repo rules

Go backend for EthicGuard. Read [../CLAUDE.md](../CLAUDE.md) and
[ARCHITECTURE.md](ARCHITECTURE.md) before making non-trivial changes.

## Hard rules (zero-retention)

Enforced at the package boundary, not just by convention:

- **`internal/store` must never persist Jira issue content.** No `summary`,
  `description`, `acceptance_criteria`, `comment_body`, or any free-form
  user-authored text on any struct that crosses into a repository.
- **`internal/jira` is in-memory only.** Its normalized output flows to
  `internal/analysis` and `internal/llm`, never to `internal/store`.
- **`internal/llm` must not log prompt bodies above `slog.LevelDebug`.**
  Prompts and responses live in memory for one call and are then discarded.
- **Findings and conflicts** carry `message_key` + `anchor`, never a copy of
  the offending text. The UI re-fetches live Jira content to render.

If you find yourself wanting to add a TEXT/JSONB column that holds user-written
content, stop and read [ARCHITECTURE.md](ARCHITECTURE.md). The answer is
almost certainly "store an anchor and a message key."

## Go conventions

- Go 1.23+. Stick to stdlib where it's reasonable; current router is
  `net/http` with 1.22+ pattern routing — do not pull in a router until
  there's a concrete reason.
- Errors: always wrap with context (`fmt.Errorf("doing X: %w", err)`); never
  swallow. Return errors, don't panic.
- Logging: `log/slog` JSON handler. Every log line should carry `request_id`
  and (when applicable) `cloud_id` and `job_id`.
- Tests: `testing` + table-driven. Test behavior at the package boundary, not
  internals. Place tests next to the code (`*_test.go`).
- Layout: business code in `internal/`, the only `main` lives in
  `cmd/ethicguard-api`. Cross-package imports flow inward; `internal/store`
  must not import `internal/httpapi` or `internal/llm`.

## Database & migrations

- Migrations live in `migrations/`, managed by `goose`. **Never edit a
  committed migration.** Add a new one with the next sequential number.
- New tables and columns are reviewed against the zero-retention rule at
  design time — call it out in the commit message.
- Schema changes that touch `findings`, `conflicts`, `jobs`, or
  `audit_log` need a paragraph in the commit body explaining why no issue
  content is being added.
- Use `pgx/v5`. Always use parameterized queries. Never string-concatenate SQL.

## Async jobs

- Workers claim with `SELECT ... FOR UPDATE SKIP LOCKED`. If you're tempted
  to add Redis or an external queue, talk to the user first — the explicit
  decision was "Postgres is the queue."
- Jobs are bounded-retry. Failures store a stable error *code* in `jobs.error`,
  never the LLM's raw error text.
- Worker concurrency is controlled by `ETHICGUARD_WORKER_CONCURRENCY`. Tests
  for worker code must not depend on real timing — use injectable clocks.

## LLM integration rules

- Use the **`claude-api` skill** when writing or debugging code in
  `internal/llm` or `internal/analysis`.
- Default model: `claude-sonnet-4-6`. `claude-opus-4-6` only when a policy
  asks for it (heavy cross-issue conflict runs).
- **Always enable prompt caching** on the system prompt + policy block — they
  are stable per installation and this is most of the cost win.
- **Always request structured JSON output** matching the schemas in
  `internal/analysis`. Validate strictly; on parse failure, fail the job
  rather than best-effort patching.
- Retries: bounded, exponential backoff, only on transient errors
  (`429`, `5xx`, network). Never retry on a 4xx other than `429`.
- Do not fall back to another provider. Claude-only is a product decision.

## Definition of done (per change)

Before claiming a task complete:

1. `go build ./...` succeeds.
2. `go test -race -count=1 ./...` is green.
3. `golangci-lint run` is clean (or you've explained the suppression).
4. New behavior has tests; new schema has a migration.
5. If the change touches storage or LLM calls, the commit body says so.

If you can't run any of these (e.g. no DB available locally), say so
explicitly in the response — do not claim "tests pass" when you skipped them.

## Files you should know

- [cmd/ethicguard-api/main.go](cmd/ethicguard-api/main.go) — wiring & shutdown
- [internal/config/config.go](internal/config/config.go) — env vars (single source)
- [internal/httpapi/router.go](internal/httpapi/router.go) — route registration
- [migrations/0001_init.sql](migrations/0001_init.sql) — schema baseline
- [ARCHITECTURE.md](ARCHITECTURE.md) — system-level contracts
