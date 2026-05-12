# EthicGuard-API — repo rules

Go backend for the EthicGuard QA intelligence platform. Read this entire file
and [ARCHITECTURE.md](ARCHITECTURE.md) before making non-trivial changes.

---

## Project context (applies to both EthicGuard repos)

EthicGuard is a Jira-native QA intelligence product: a bridge between Jira and
Anthropic Claude. It lives in two side-by-side repos:

- **EthicGuard-API** (this repo) — Go backend, Postgres, Claude Agent SDK
- **EthicGuard-UI** — Atlassian Forge app, TypeScript

The same cross-repo rules below are duplicated into the UI repo's `CLAUDE.md`
so each side is self-contained when opened independently.

### The non-negotiable rule

**Zero customer-data retention.** EthicGuard-API must never persist Jira issue
content (titles, descriptions, AC text, comments, attachments). Allowed in
Postgres: ids, issue keys, severity/score, anchors (`{field, start, end}`),
stable message keys, audit metadata. Anything else is a bug.

If a feature seems to need stored issue text, the right answer is almost
always "store an anchor + message key and re-fetch live on render." See
[ARCHITECTURE.md](ARCHITECTURE.md).

### Stack is locked

Do not propose alternatives unless explicitly asked:

- LLM: **Claude only**, via the Anthropic SDK / Claude Agent SDK patterns.
  Default model `claude-sonnet-4-6`; heavy model `claude-opus-4-6`, opt-in per policy.
- API language: **Go 1.23+**.
- Jira integration: **Atlassian Forge** (Jira Cloud only).
- Persistence: **Postgres 16**, single managed instance. No Redis, no extra stores.
- Async: in-process Go worker pool over a Postgres job table. No external queue.
- Hosting: single container on Fly.io / Railway / Render.

Out of scope: multi-LLM abstraction, Jira Data Center / Server, custom
embeddings, fine-tuning, Marketplace billing, SSO beyond Forge identity.

### Cross-repo conventions

- Conventional commits (`feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`, `ci:`).
- Branch names: `type/short-description`.
- The OpenAPI spec at `api/openapi.yaml` is the source of truth for the API
  contract. UI types are generated from it — never hand-edit both sides.
- A breaking API change bumps the path prefix (`/v1` → `/v2`); never silently
  reshape an existing endpoint.

### Skills and agents to use

| When you're doing… | Use |
|---|---|
| Writing or debugging Anthropic SDK code, prompt caching, structured output | **`claude-api` skill** |
| Reviewing changed code for reuse, simplicity, dead code | **`simplify` skill** |
| Capturing a non-obvious lesson after a tricky fix | **`/learn`** (writes to `memory/`) |
| Open-ended search across the repo | **`Explore` subagent** |
| Designing the implementation for a non-trivial feature | **`Plan` subagent** |
| Questions about Forge, Anthropic SDK, or Claude Code itself | **`claude-code-guide` subagent** |
| Looking up library docs (pgx, goose, anthropic-sdk-go, Forge) | **`context7` MCP** — prefer over web search |

### Working agreements

- Before claiming a task complete, run the repo's tests and lint. If you can't,
  say so explicitly.
- Never edit a committed migration. Add a new one.
- Never log prompt bodies or normalized issue payloads above debug level.
- Never commit `.env`, secrets, or anything matching `*credentials*`.
- Project-specific lessons go in `memory/` via `/learn`.
- **Keep `EthicGuard-API/ARCHITECTURE.md` diagrams current.** Any change that
  alters deploy topology, request flow, auth flow, data model, or LLM wiring
  must update the diagrams at the top of that file in the same commit. Before
  replacing a diagram, move the outgoing version into the `## Evolution`
  section at the bottom with today's date and a one-line reason. Never delete
  past diagrams — they are the historical record of how the system grew.

---

## Repo-specific: zero-retention enforcement at the package boundary

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

**As of the Rovo-as-brain pivot, the per-issue analysis path no longer runs
through this API.** Atlassian Rovo (declared in
`EthicGuard-UI/manifest.yml`) does the analysis itself; the UI's
`stampLabel` resolver POSTs the result to
[POST /v1/analysis/results](internal/httpapi/analysis.go) for persistence.

That makes the following packages **orphaned for the per-issue path but
kept for future batch work** (cross-issue conflict detection, project-wide
re-scans):

- `internal/llm` — the Anthropic SDK wrapper. No callers from the per-issue
  path. Future batch worker re-uses it as-is.
- `internal/analysis` — `Run`, `RunOptions`, prompt construction. Same:
  orphaned now, batch reuses.
- `internal/worker` — the goroutine pool that drains `POST /v1/analysis`
  enqueues. Still wired in `cmd/ethicguard-api`; no per-issue traffic
  reaches it post-pivot.

The rules below still apply when (a) batch work resumes or (b) you touch
any of those packages:

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

## Code conventions (enforced by review)

Short, grep-friendly rules. Each maps to a concrete pattern in the code;
see [docs/CONVENTIONS.md](docs/CONVENTIONS.md) for one representative
example file per rule.

1. **HTTP errors go through the helpers in [internal/httpapi/errors.go](internal/httpapi/errors.go)** — `badRequest`, `notFound`, `unauthorized`, `methodNotAllowed`, `internalErr`, `forbidden`. Never write `map[string]string{"error": ...}` inline in a handler.
2. **Validation limits live in [internal/httpapi/validation.go](internal/httpapi/validation.go).** No `const max…` inline in a handler file.
3. **Severity comparisons go through `analysis.Severity.AtLeast`.** No inline `switch severity` outside [internal/analysis/labels.go](internal/analysis/labels.go).
4. **Use `store.IsNotFound(err)`**, not `errors.Is(err, store.ErrNotFound)`. One spelling, grep-friendly.
5. **Every LLM call runs under a `context.WithTimeout`.** Workers do this in [internal/worker/pool.go](internal/worker/pool.go); any new caller of `analysis.Run` does the same.
6. **Every package under `internal/` carries a `// Package …` doc comment** stating its one invariant (e.g. zero-retention boundary, env-var single source).
7. **Constructors in `internal/store` never return nil without an error.** Callers do not nil-check return values.
8. **Handler tests use `httptest` + the real router from `httpapi.NewRouter`,** not a hand-built `http.HandlerFunc`. Matches the pattern in `httpapi/projects_test.go`.

## OpenAPI types

The cross-repo conventions block above claims `api/openapi.yaml` is the
source of truth. **That file does not yet exist.** Until it does:

- The UI types in `EthicGuard-UI/src/api/client.ts` and
  `EthicGuard-UI/src/resolvers/*.ts` are **hand-written**. Treat the Go
  request/response structs in `internal/httpapi/*.go` as the contract.
- **When an API shape changes**, the UI types change in the same PR set.
- The `openapi-typescript` generator is wired on the UI side but pointed
  at a missing file; bringing it online is its own task.

## Definition of done (per change)

Before claiming a task complete:

1. `make verify` is green. Runs `go build ./...`, `go test -race -count=1 ./...`,
   `golangci-lint run`, and the per-package coverage gate (`make cover-check`),
   which fails on any covered package below the **70% floor**. Excluded
   packages live in [scripts/cover-check/main.go](scripts/cover-check/main.go);
   add a new exclusion only with a justification in this file's "Code
   conventions" section.
2. New behavior has tests; new schema has a migration.
3. If the change touches storage or LLM calls, the commit body says so.

If you can't run any of these (e.g. no DB available locally), say so
explicitly in the response — do not claim "tests pass" when you skipped them.

**70% is a floor, not a target.** Reviews still look at *what* is tested, not
just the percentage — the gate stops drift, it doesn't guarantee quality.

## Files you should know

- [cmd/ethicguard-api/main.go](cmd/ethicguard-api/main.go) — wiring & shutdown, repo map at top
- [internal/config/config.go](internal/config/config.go) — env vars (single source)
- [internal/httpapi/router.go](internal/httpapi/router.go) — route registration
- [internal/httpapi/errors.go](internal/httpapi/errors.go) — HTTP error envelope
- [internal/httpapi/validation.go](internal/httpapi/validation.go) — request-validation limits
- [internal/analysis/labels.go](internal/analysis/labels.go) — severity rank + AC label decision rule
- [internal/store/errors.go](internal/store/errors.go) — `IsNotFound` helper
- [migrations/0001_init.sql](migrations/0001_init.sql) — schema baseline
- [docs/CONVENTIONS.md](docs/CONVENTIONS.md) — the 8 review-enforced rules with example links
- [ARCHITECTURE.md](ARCHITECTURE.md) — system-level contracts
