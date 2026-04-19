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

## Architecture diagrams

The canonical picture of EthicGuard as it runs today (2026-04-19). Three
views, each capturing a different concern. Older versions of these diagrams
are preserved in the [Evolution](#evolution) section at the bottom of this
file — never deleted, so the history of how the system grew stays readable.

> **Rule:** any change that alters deploy topology, request flow, auth flow,
> data model, or LLM wiring must update these diagrams in the same commit,
> and move the outgoing version into the Evolution section with today's date
> and a one-line reason. This rule is also duplicated into each repo's
> `CLAUDE.md` so future sessions enforce it.

### Deploy topology

Where everything runs and who talks to whom.

```
                    ┌──────────────────────────────────────────┐
                    │              End users                    │
                    │  (browsers: devs, Jira admins, visitors)  │
                    └───────┬────────────────────────┬──────────┘
                            │                        │
            ┌───────────────┤                        ├──────────────────┐
            │ marketing     │                        │ working in Jira  │
            ▼               │                        ▼                  │
   ┌──────────────────┐     │              ┌─────────────────────────┐  │
   │  ethicguard.ai   │     │              │ ethicguard.atlassian.net│  │
   │  www.*           │     │              │ (Jira Cloud site)       │  │
   │                  │     │              └────────────┬────────────┘  │
   │ Render static    │     │                           │ invokes        │
   │ site             │     │                           ▼                │
   │ (Astro 5 +       │     │              ┌─────────────────────────┐  │
   │  Tailwind 3,     │     │              │ Atlassian Forge runtime │◄─┘
   │  global CDN)     │     │              │ EthicGuard-UI, Node 22  │
   └──────────────────┘     │              │                         │
                            │              │ modules                 │
                            │              │  ├─ jira:issuePanel     │
                            │              │  ├─ jira:issueAction    │
                            │              │  ├─ jira:issueContext   │
                            │              │  ├─ jira:projectSetting │
                            │              │  └─ graph Custom UI     │
                            │              │                         │
                            │              │ resolvers               │
                            │              │  ├─ runQaReview         │
                            │              │  ├─ getJobStatus        │
                            │              │  └─ getIssueGraph       │
                            │              │                         │
                            │              │ triggers (lifecycle)    │
                            │              │  ├─ installed           │
                            │              │  └─ uninstalled         │
                            │              │                         │
                            │              │ per-install storage     │
                            │              │  └─ shared HS256 secret │
                            │              └────────────┬────────────┘
                            │                           │ HTTPS
                            │                           │ Bearer HS256 JWT
                            │                           │ (cloudId claim)
                            │                           ▼
                            │           ┌──────────────────────────────────┐
                            │           │ api.ethicguard.ai                │
                            │           │ Render Docker web service        │
                            │           │ ethicguard-api-cvvk, Frankfurt   │
                            │           │ Go 1.25 + net/http               │
                            │           │                                  │
                            │           │ middleware                       │
                            │           │  └─ HS256 JWT verify + install   │
                            │           │     lookup by cloudId            │
                            │           │                                  │
                            │           │ routes                           │
                            │           │  ├─ GET  /v1/health              │
                            │           │  ├─ GET  /v1/version             │
                            │           │  ├─ POST /v1/installations/      │
                            │           │  │         lifecycle (installer  │
                            │           │  │         JWT auth)             │
                            │           │  └─ POST /v1/analysis (authed)   │
                            │           └────────┬──────────────────┬──────┘
                            │                    │ pgx              │ HTTPS
                            │                    ▼                  ▼
                            │       ┌─────────────────────┐  ┌───────────────┐
                            │       │ Postgres 16         │  │ Anthropic API │
                            │       │ ethicguard-pg-cvvk  │  │ Claude        │
                            │       │ Frankfurt           │  │ Sonnet 4.6    │
                            │       │                     │  │               │
                            │       │ current tables used:│  │ prompt cached │
                            │       │  └─ installations   │  │ on system     │
                            │       │     (cloud_id +     │  │ prompt        │
                            │       │      shared_secret, │  │ (ephemeral)   │
                            │       │      NO issue text) │  │               │
                            │       └─────────────────────┘  └───────────────┘
                            │
                            ▼ DNS
                    ┌──────────────────────────────┐
                    │ Porkbun registrar             │
                    │ + Cloudflare-backed DNS       │
                    │  ALIAS @ → ethicguard-web     │
                    │  CNAME www → ethicguard-web   │
                    │  CNAME api → ethicguard-api-  │
                    │              cvvk             │
                    └──────────────────────────────┘
```

### Request flow — "Run QA Review"

The core product path: user → Jira → Forge → API → Claude → findings.

```
User clicks "Run QA Review" in Jira
  │
  ▼
Forge runtime invokes resolver runQaReview
  │
  ├─► @forge/api.asApp().requestJira(/rest/api/3/issue/{key})
  │   (scope: read:jira-work)
  │
  ├─► normalizeIssue(raw)                    in-memory only, never persisted
  │
  ├─► storage.get(sharedSecret)              per-install Forge storage
  │
  ├─► mintApiToken(cloudId)                  HS256, aud=ethicguard-api,
  │                                          ttl=300s, signed with shared secret
  │
  └─► POST https://api.ethicguard.ai/v1/analysis
      Authorization: Bearer <JWT>
      body: { issueKey, projectKey, kind: "ac_quality",
              payload: NormalizedIssue }
      │
      ▼
      auth.Middleware
      ├─ peekCloudID(token)                   unverified cloudId peek
      ├─ installations.GetByCloudID(cloudID)  Postgres SELECT
      ├─ auth.Verify(token, inst.SharedSecret, AudienceAPI)
      └─ ctx = WithValue(ctx, *Installation)
      │
      ▼
      AnalysisHandler.ServeHTTP
      ├─ decode json.Body → analysis.AnalysisRequest
      └─ analysis.Run(ctx, llm, req)
          ├─ formatUserContent(payload)       markdown, in-memory
          ├─ llm.Analyze(ctx, systemPrompt, userContent)
          │   └─► anthropic.Client.Messages.New
          │       ├─ model:     claude-sonnet-4-6
          │       ├─ maxTokens: 4096
          │       ├─ system:    QA policy (CacheControl: ephemeral)
          │       └─ user:      serialized issue content
          │   ◄── JSON array of findings in a text block
          ├─ stripCodeFence(raw)
          └─ json.Unmarshal → []Finding
      │
      ▼
      writeJSON(200, { findings: Finding[] })
  │
  ▼
Forge runtime returns findings to the Jira UI Kit view
  │
  ▼
User sees structured findings rendered in the EthicGuard panel.
```

### Install / uninstall flow

How a freshly installed Forge app establishes a per-installation shared
secret with the API before it can make any authenticated call.

```
forge install  (or Marketplace install from a Jira admin)
  │
  ▼
Atlassian fires   avi:forge:installed:app
  │
  ▼
Forge trigger lifecycle-installed → onInstalled(payload)
  │
  ├─► crypto.randomBytes(32).toString("hex")        new 32-byte shared secret
  ├─► storage.set(sharedSecret, <hex>)              per-install Forge storage
  ├─► mintInstallerToken(cloudId)                   HS256, aud=ethicguard-installer,
  │                                                 signed with INSTALLER_SECRET
  │                                                 (pre-shared Forge variable)
  │
  └─► POST https://api.ethicguard.ai/v1/installations/lifecycle
      Authorization: Bearer <installer JWT>
      body: { event: "install", cloudId, sharedSecret: <hex> }
      │
      ▼
      LifecycleHandler
      ├─ auth.Verify(token, ETHICGUARD_INSTALLER_SECRET, AudienceInstaller)
      ├─ require claims.CloudID == body.cloudId
      ├─ installations.Upsert(cloudId, sharedSecret)   Postgres ON CONFLICT UPDATE
      └─ writeJSON(200, { status: "installed" })

Subsequent resolver calls now have a working per-install shared secret on
both sides; normal JWT auth path kicks in.

─────────────────────────────────────────────────────────────────────

forge uninstall  (or user removes the app from Jira)
  │
  ▼
Atlassian fires   avi:forge:uninstalled:app
  │
  ▼
Forge trigger lifecycle-uninstalled → onUninstalled(payload)
  │
  ├─► storage.delete(sharedSecret)                   best-effort cleanup
  └─► POST /v1/installations/lifecycle (installer JWT)
      body: { event: "uninstall", cloudId }
      │
      ▼
      LifecycleHandler
      ├─ installations.DeleteByCloudID(cloudID)
      │   → CASCADE drops every projects / jobs / findings /
      │     conflicts / audit_log row tied to this cloudId.
      └─ writeJSON(200, { status: "uninstalled" })

Zero-retention guarantee: after uninstall, nothing about this installation
remains in the database.
```

### Status of the pipeline (as of 2026-04-19)

Implemented and deployed:

- Marketing site at `ethicguard.ai` (Astro, Render static)
- Forge UI v3.3.0 on `ethicguard.atlassian.net` — all modules render
- `api.ethicguard.ai` on Render Docker with managed Postgres in Frankfurt
- Auth: HS256 JWTs + installations table + lifecycle webhook
- `/v1/analysis`: synchronous call to Claude Sonnet 4.6, returns findings
  directly (no job queue yet)
- `installations` table populated on install, cascaded on uninstall

Designed but not yet implemented:

- Job queue + worker pool (`jobs` table schema exists; API currently runs
  analysis synchronously in-handler)
- Project / company policy evaluation (`policies`, `projects` tables exist;
  a single hardcoded system prompt is used for now)
- `findings` / `conflicts` / `audit_log` persistence (schema exists, no
  writes happen yet — the handler returns findings directly without
  persisting)
- Cross-issue conflict detection (needs linked-issue fetch + larger context)
- OpenAPI spec generation and type-safe UI client

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

## Evolution

Old architecture diagrams live here, newest first. Any change that replaces a
diagram above must move the outgoing version here with a date and a one-line
reason. Diagrams are append-only — the history of how the system grew is
worth more than a clean slate.

### v0 — initial plan (2026-04-13)

The pre-implementation sketch from the scaffold day. Captured the product
intent but predated every actual deploy:

- wrote "Node 20" for the Forge runtime (now Node 22)
- described an asynchronous worker-pool analysis pipeline with a Postgres
  job queue; the real MVP skipped that and went synchronous
- didn't name the hosts (`ethicguard.ai`, `api.ethicguard.ai`), because
  the marketing site + domain + Render deploy didn't exist yet
- didn't include the graph module, issue-context module, or lifecycle
  trigger — those all came later

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
