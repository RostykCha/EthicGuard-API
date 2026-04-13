# EthicGuard Architecture

This document is the source of truth for how EthicGuard-API and
EthicGuard-UI fit together. If you're making a change that touches
data flow, auth, persistence, or LLM calls, read this first.

## Product framing

EthicGuard is a **bridge between Jira and Claude**. Jira stays the source of
truth for issues, epics, and workflow state. EthicGuard reads issue content
through a Forge app, sends it to an Anthropic Claude model via the Claude
Agent SDK, and writes back structured QA findings into its own small store.
The UI renders those findings inside the Jira issue view.

## High-level diagram

```
 Jira Cloud
    │  (issue view, issue action "Run QA Review")
    ▼
 EthicGuard-UI  (Forge app, Node 20, TypeScript)
    │  resolvers run inside the Forge runtime
    │  ├─ read Jira issue via @forge/api  (scope: product.jira:read)
    │  └─ POST analysis request → EthicGuard-API  (signed JWT, cloudId claim)
    ▼
 EthicGuard-API  (Go, stateless, single binary)
    ├─ http handlers  → validate JWT, enqueue job, return jobId
    ├─ workers        → pull job, run Claude Agent SDK, normalize findings
    ├─ policy engine  → project/company QA policies gate/score findings
    ├─ postgres       → installations, projects, policies, jobs, findings, audit
    └─ Claude API     → Sonnet 4.6 default, Opus 4.6 opt-in per policy
```

## The zero-retention rule

> EthicGuard-API **never** persists Jira issue content.

Specifically, these fields are forbidden in any row written to Postgres:

- Issue summary / title
- Issue description
- Acceptance criteria text (any custom field)
- Comments
- Attachments
- Any user-authored free-form text from the issue

What *is* allowed to be persisted:

- Numeric ids (row ids, installation ids, project ids, job ids)
- Issue keys (e.g. `PROJ-123`) — these are identifiers, not content
- Severity and score values
- Category / kind enums
- Anchors — structured pointers like `{"field":"description","start":120,"end":180}`
- Message keys — stable strings (e.g. `ambiguity.vague_quantifier`) that resolve
  to human text in a static catalog in the API repo
- Audit metadata — actor account id, action name, timestamps

Why the split: message keys + anchors let the UI reconstruct human-readable
findings by re-fetching live Jira content on render, while the database stays
free of any customer text. If an installation is uninstalled, a `DELETE` on
`installations` cascades and leaves nothing recoverable.

Concretely:

- `internal/store` must reject any struct that embeds raw issue text.
- `internal/jira` normalizes inbound payloads for in-memory use only; the
  normalized form must not flow into `internal/store`.
- `internal/llm` holds prompt text in memory for a single call and discards
  it; prompt bodies are never logged at info level.

## Auth model

EthicGuard-UI runs in the Forge runtime. On every API call:

1. A Forge resolver mints a short-lived JWT using a per-installation shared
   secret that was saved during the Forge install lifecycle event.
2. The JWT carries `cloudId`, audience `ethicguard-api`, and a short `exp`.
3. EthicGuard-API middleware verifies the signature against the stored
   shared secret for that `cloudId` and loads the `installations` row.
4. Handlers receive the installation in the request context.

No end-user OAuth, no 3LO. The blast radius of a leaked shared secret is
scoped to a single installation and can be rotated via the lifecycle webhook.

## Analysis lifecycle

Happy path for a single issue:

1. User clicks **Run QA Review** in Jira (issue action).
2. Forge resolver reads the issue via `@forge/api` (`product.jira:read`),
   normalizes it, mints a JWT, and `POST`s to `/v1/analysis`.
3. API verifies the JWT, writes a `jobs` row with status `queued`, and
   returns `{ jobId }` within a few milliseconds.
4. A worker claims the job (`SELECT ... FOR UPDATE SKIP LOCKED`), marks it
   `running`, and builds a prompt from the active policy + the normalized
   payload provided in the request body.
5. Worker calls Claude via the Agent SDK. System prompt + policy block are
   marked as cacheable so repeated calls in the same installation are cheap.
6. Worker parses the model's structured output, runs the policy evaluator,
   and writes `findings` / `conflicts` rows. No issue text is written.
7. Worker marks the job `done`. An `audit_log` row records the action.
8. The UI polls `GET /v1/analysis/{jobId}` until status is `done`, then
   renders findings, re-fetching any needed issue text live from Jira.

Failure handling: any error in step 4–6 marks the job `failed`, stores an
error *code* in `jobs.error`, and increments a retry counter on the job row
(retries are bounded).

## Data model

See [migrations/0001_init.sql](migrations/0001_init.sql) for the authoritative
schema. Summary:

- `installations` — one row per Jira Cloud install, shared secret, timestamps
- `projects` — one row per Jira project we've seen
- `policies` — company- or project-scoped QA policy JSON
- `jobs` — analysis job queue; status only, **no issue content**
- `findings` — per-issue findings; ids, severity, score, anchor, message key
- `conflicts` — cross-issue conflict findings; references issue keys only
- `audit_log` — who did what, when

## Async job queue

We use Postgres as the queue to avoid a Redis dependency in the MVP:

```sql
UPDATE jobs SET status = 'running', started_at = NOW()
 WHERE id = (
   SELECT id FROM jobs
    WHERE status = 'queued'
    ORDER BY created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
 )
 RETURNING *;
```

Workers are a pool of goroutines inside the same binary, sized by
`ETHICGUARD_WORKER_CONCURRENCY`. Horizontal scaling: run more API replicas;
`SKIP LOCKED` ensures they don't fight over the same job.

## LLM integration

- SDK: official Anthropic Go SDK, using the Agent SDK patterns.
- Default model: `claude-sonnet-4-6`. Heavy model: `claude-opus-4-6`,
  opt-in per policy for cross-issue conflict runs.
- Prompt caching: the system prompt + policy block are marked cacheable.
  These are stable per-installation, so the TTL buys us near-free repeat
  calls within the same analysis burst.
- Structured output: we ask the model for JSON matching `internal/analysis`
  schemas and validate before writing rows. Invalid outputs fail the job
  rather than being best-effort parsed.

## Failure modes to keep in mind

- **LLM timeouts / rate limits.** Job goes to `failed` with an error code;
  UI shows a retry button. Do not fall back to another provider.
- **Forge egress timeout (~25s).** The UI polls — the synchronous POST
  only has to enqueue.
- **Shared secret rotation.** Lifecycle webhook replaces the row atomically.
  In-flight requests with an old JWT will fail verification; the UI will
  redrive them.
- **Schema drift between API and UI.** The OpenAPI spec at `api/openapi.yaml`
  is checked in and consumed by the UI. Breaking changes bump the path
  prefix (e.g. `/v2`).

## Deployment

MVP target: a single container on Fly.io / Railway / Render with a managed
Postgres. The binary is stateless; configuration comes from environment
variables. See `Dockerfile` and `.env.example`.

## What's intentionally out of scope

- Multi-LLM abstraction (Claude-only, by design)
- Jira Data Center / Server (Cloud only)
- SSO beyond Forge's built-in identity
- Custom embeddings / fine-tuned models
- Billing and Marketplace listing
