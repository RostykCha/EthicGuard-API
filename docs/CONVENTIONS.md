# API conventions (review-enforced)

Eight rules. Each links to one representative example in the codebase. If
you find yourself writing a pattern that doesn't match the example, change
your code — don't add a new pattern.

The rationale lives in [CLAUDE.md](../CLAUDE.md). This file exists for grep
and for the future Claude session that wants the rule without the context.

---

### 1. HTTP errors go through `httpapi.writeErr` / helpers

Helpers: `badRequest`, `notFound`, `unauthorized`, `methodNotAllowed`, `internalErr`, `forbidden`.

- Defined in: [internal/httpapi/errors.go](../internal/httpapi/errors.go)
- Example: [internal/httpapi/projects.go](../internal/httpapi/projects.go) — every error path.

Never write `writeJSON(w, http.Status…, map[string]string{"error": …})` inline.

---

### 2. Validation limits live in one file

- Defined in: [internal/httpapi/validation.go](../internal/httpapi/validation.go) (`maxIssueTypes`, `maxIssueTypeIDLen`, `maxPromptAddendumBytes`, `isValidSeverity`).

No `const max…` inline in a handler file. New limit? Add it here and import.

---

### 3. Severity comparisons go through `analysis.Severity.AtLeast`

- Defined in: [internal/analysis/labels.go](../internal/analysis/labels.go) (`type Severity string`, `Rank()`, `AtLeast()`, `FilterBySeverity`).
- Example: `FilterBySeverity` in the same file shows the boundary conversion (`Severity(f.Severity).AtLeast(t)`).

Don't write `switch sev { case "high": … }` outside `labels.go`.

---

### 4. Use `store.IsNotFound(err)` instead of `errors.Is(err, store.ErrNotFound)`

- Defined in: [internal/store/errors.go](../internal/store/errors.go).
- Example: [internal/httpapi/jobs.go](../internal/httpapi/jobs.go) — `if store.IsNotFound(err) { … }`.

Negative form `!store.IsNotFound(err)` reads identically. One spelling to grep for.

---

### 5. Every LLM call runs under a `context.WithTimeout`

- Defined in: [internal/worker/pool.go](../internal/worker/pool.go) — `processJob` wraps `analysis.Run` in `context.WithTimeout(p.deps.JobTimeout)`.
- Timeout is configured via `ETHICGUARD_JOB_TIMEOUT` (default `90s`).

New caller of `analysis.Run`? Wrap your context. Distinguish `context.DeadlineExceeded` → stable code `"timeout"`.

---

### 6. Package-level `// Package …` doc comment on every `internal/*`

Comment must state:
- What the package owns
- One invariant the package enforces (e.g., "Postgres never sees Jira content")

- Examples:
  - [internal/store/pg.go](../internal/store/pg.go) — zero-retention.
  - [internal/httpapi/router.go](../internal/httpapi/router.go) — handlers depend on interfaces, not concretes.
  - [internal/worker/pool.go](../internal/worker/pool.go) — claim-from-Postgres-then-payload-bus pattern.

---

### 7. Constructors in `internal/store` never return nil without an error

- Examples: `store.Open` returns `(*Store, error)`, never `nil, nil`.

Callers don't nil-check return values. If a constructor cannot run (missing config, etc.), it returns a non-nil error.

---

### 8. Handler tests use `httptest` + the real router

- Example: [internal/httpapi/projects_test.go](../internal/httpapi/projects_test.go) — wires the real router via `httpapi.NewRouter(...)` with fake repos.

Don't hand-build an `http.HandlerFunc` for tests. Going through the router catches middleware regressions.
