# EthicGuard-API

Backend service for **EthicGuard**, a Jira-native QA intelligence platform.
EthicGuard-API analyzes Jira issue content, detects acceptance-criteria gaps
and cross-issue conflicts, generates QA scenarios, and returns structured
findings to the [EthicGuard-UI](../EthicGuard-UI) Forge app.

## At a glance

- **Language:** Go 1.23+
- **LLM:** Anthropic Claude via the Claude Agent SDK (default model **Sonnet 4.6**)
- **Datastore:** Postgres 16 — **zero customer-data retention** (see [ARCHITECTURE.md](ARCHITECTURE.md))
- **Async:** in-process worker pool backed by a Postgres job table
- **Auth:** Forge-minted JWTs, per-installation shared secret

## Architecture at a glance

```
Browser ──► ethicguard.ai  (Render static site, Astro)
Browser ──► ethicguard.atlassian.net ──► Forge UI (Node 22)
                                              │
                                              │ HS256 JWT
                                              ▼
                                         api.ethicguard.ai
                                         (Render Docker, Frankfurt)
                                              │
                                    ┌─────────┴──────────┐
                                    ▼                    ▼
                              Postgres 16          Claude Sonnet 4.6
                              (installations       (Anthropic API)
                               only, no issue
                               text)
```

Full topology, request flow, install flow, and evolution history live in
[ARCHITECTURE.md](ARCHITECTURE.md). **Any change that touches deploy topology,
request flow, auth, data model, or LLM wiring must update those diagrams in
the same commit** — the rule is also spelled out in every repo's
[CLAUDE.md](CLAUDE.md).

## Zero-retention guarantee

EthicGuard-API **never** persists Jira issue titles, bodies, descriptions,
acceptance criteria, or comments. Issue content lives in a worker's memory
only for the duration of a single analysis call. Stored rows in `findings`,
`conflicts`, `jobs`, and `audit_log` contain ids, scores, anchors, and
references — nothing that could leak customer content.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full data-flow and schema.

## Quickstart (local dev)

Prerequisites: Go 1.23+, Docker (for Postgres), and an Anthropic API key.

```bash
cp .env.example .env                   # fill in ETHICGUARD_ANTHROPIC_API_KEY
docker run --rm -d --name ethicguard-pg \
  -e POSTGRES_USER=ethicguard -e POSTGRES_PASSWORD=ethicguard \
  -e POSTGRES_DB=ethicguard -p 5432:5432 postgres:16
make run
curl http://localhost:8080/v1/health   # -> {"status":"ok"}
```

## Common commands

| Command        | What it does                                |
|----------------|---------------------------------------------|
| `make run`     | Run the API locally                         |
| `make test`    | Run unit tests                              |
| `make build`   | Compile the binary                          |
| `make lint`    | Run `golangci-lint`                         |
| `make tidy`    | Tidy `go.mod` / `go.sum`                    |

## Project layout

```
cmd/ethicguard-api   entrypoint: wiring, shutdown
internal/config      env parsing & validation
internal/httpapi     chi-style router, middleware, handlers
internal/auth        Forge JWT verification
internal/jira        Jira payload normalization (in-memory only)
internal/analysis    prompt builders, finding schema, result normalization
internal/policy      company/project QA policies + evaluator
internal/llm         Anthropic SDK wrapper, prompt caching, retries
internal/jobs        Postgres-backed queue + worker pool
internal/store       pgx repositories (no issue content)
internal/audit       structured audit log writes
internal/version     build info
migrations           goose SQL migrations
api/openapi.yaml     generated spec consumed by EthicGuard-UI
```

## Environment variables

See [.env.example](.env.example). Secrets (`ETHICGUARD_ANTHROPIC_API_KEY`,
`ETHICGUARD_DATABASE_URL`) must be supplied via environment or a local `.env`
file — never committed.

## API surface (v1)

All routes are under `/v1` and require a Forge-minted JWT (except `/health`
and `/version`).

| Method | Path                                | Purpose                                |
|--------|-------------------------------------|----------------------------------------|
| POST   | `/v1/installations/lifecycle`       | Forge install / uninstall webhook      |
| POST   | `/v1/analysis`                      | Kick off an analysis, returns `jobId`  |
| GET    | `/v1/analysis/{jobId}`              | Poll job status and findings refs      |
| GET    | `/v1/projects/{projectKey}/policy`  | Read project QA policy                 |
| PUT    | `/v1/projects/{projectKey}/policy`  | Update project QA policy               |
| GET    | `/v1/policies/company`              | Read company QA policy                 |
| PUT    | `/v1/policies/company`              | Update company QA policy               |
| GET    | `/v1/health`                        | Liveness probe                         |
| GET    | `/v1/version`                       | Build info                             |

## Contributing

- Conventional commits (`feat:`, `fix:`, `refactor:`, …).
- Unit tests for new business logic — table-driven in Go.
- Lint and tests must pass in CI before merge.
- Never add code that stores Jira issue content. If you think you need to,
  read [ARCHITECTURE.md](ARCHITECTURE.md) first and open an issue.
