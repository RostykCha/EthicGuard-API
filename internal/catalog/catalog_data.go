package catalog

// rawEntries is the authoritative list of finding message keys. Keep this in
// sync with the allowed messageKey enum in internal/analysis/analyze.go —
// the catalog_test.go assertion fails loudly if they drift.
//
// Each entry carries up to four variants:
//   - Default: the neutral fallback.
//   - PM: framed as release risk / stakeholder impact.
//   - QA: framed as test coverage / verifiable outcome.
//   - Dev: framed as code path / guard clause.
//
// The status handler picks a variant by the `role` query param (Phase 2 #9).
// When a variant is empty, the caller's Resolve() transparently falls back
// to Default — so it is safe to leave role variants blank while the product
// team iterates on copy.
var rawEntries = map[string]rawEntry{
	"ambiguity.vague_quantifier": {
		Params:  []string{"field", "term"},
		Default: `Vague quantifier in {{.field}} (e.g. "{{.term}}") — replace with a concrete value.`,
		PM:      `"{{.term}}" in {{.field}} will be interpreted differently by different teams — name an exact number or range before this ships.`,
		QA:      `"{{.term}}" in {{.field}} is not testable. Replace with a specific count or threshold you can assert on.`,
		Dev:     `"{{.term}}" in {{.field}} leaves the loop/guard bound undefined. Pick a constant or a named config value.`,
	},
	"ambiguity.unclear_pronoun": {
		Params:  []string{"field"},
		Default: `Unclear pronoun reference in {{.field}} — the subject of the sentence is ambiguous.`,
		PM:      `{{.field}} uses "it" or "they" without a clear antecedent — two readers will resolve it two ways.`,
		QA:      `Pronoun in {{.field}} could refer to multiple things; a test case can't be written against either interpretation.`,
		Dev:     `Pronoun in {{.field}} is ambiguous — name the subject explicitly so the implementer doesn't guess.`,
	},
	"ambiguity.missing_precondition": {
		Params:  []string{"field"},
		Default: `Missing precondition in {{.field}} — the behavior depends on state that is not defined.`,
		PM:      `{{.field}} assumes a prior state that isn't spelled out — users who arrive from a different flow will see different behavior.`,
		QA:      `{{.field}} depends on setup state that isn't declared. Add a Given clause or link the prerequisite.`,
		Dev:     `{{.field}} assumes state that isn't enforced by a guard — add a precondition check or document the caller contract.`,
	},
	"missing_edge_case.empty_input": {
		Params:  []string{"field"},
		Default: `No handling described for empty input in {{.field}}.`,
		PM:      `{{.field}} doesn't say what happens when the user submits nothing — first-time empty states are a common drop-off.`,
		QA:      `Empty-input case in {{.field}} is uncovered. Add a test for the zero-row / empty-string path.`,
		Dev:     `Empty input in {{.field}} has no defined branch. Decide between early-return, zero-value, or error.`,
	},
	"missing_edge_case.boundary": {
		Params:  []string{"field"},
		Default: `Boundary condition not covered in {{.field}} — consider min/max values and off-by-one.`,
		PM:      `{{.field}} leaves the min and max behavior unspecified — edge users will hit either in the first week.`,
		QA:      `{{.field}} has no boundary test. Cover min, max, and one-beyond for both ends.`,
		Dev:     `{{.field}} is silent on boundary behavior — pick inclusive or exclusive and add the assertion.`,
	},
	"missing_edge_case.concurrency": {
		Params:  []string{"field"},
		Default: `Concurrent-user effect not addressed in {{.field}} — specify expected behavior under contention.`,
		PM:      `{{.field}} doesn't say what happens when two users act at the same time — this is the classic "support ticket on day two" shape.`,
		QA:      `{{.field}} has no concurrent-write test. Pin the expected semantics (last-writer-wins, reject, merge) so a scenario is testable.`,
		Dev:     `{{.field}} ignores concurrency — decide the locking/ordering semantics and document them at the handler boundary.`,
	},
	"missing_negative_case.permission": {
		Params:  []string{"field"},
		Default: `No permission / unauthorized case described in {{.field}}.`,
		PM:      `{{.field}} doesn't say what an unauthorized user sees — this is usually what auditors ask about first.`,
		QA:      `{{.field}} is missing the unauthorized case. Add a negative scenario for a user without the required role.`,
		Dev:     `{{.field}} skips the permission-denied branch. Return 403 or the UI's no-access view with a consistent shape.`,
	},
	"missing_negative_case.error_path": {
		Params:  []string{"field"},
		Default: `No error path described in {{.field}} — what happens on failure?`,
		PM:      `{{.field}} only describes the happy path — users who hit the error today will see whatever the framework defaults to.`,
		QA:      `Failure path in {{.field}} is uncovered. Define the expected user-visible behavior on error and add a scenario.`,
		Dev:     `{{.field}} has no error branch — decide between retry, fail fast, and degraded mode, then wire the return.`,
	},
	"untestable.subjective": {
		Params:  []string{"field"},
		Default: `Subjective assertion in {{.field}} — rewrite as a verifiable outcome.`,
		PM:      `{{.field}} uses a subjective quality word — release readiness becomes a judgement call instead of a check.`,
		QA:      `{{.field}} is subjective — rewrite into a measurable outcome we can assert against.`,
		Dev:     `{{.field}} asks for "feel" rather than a checkable property — request a numeric or boolean success criterion.`,
	},
	"untestable.missing_threshold": {
		Params:  []string{"field", "metric"},
		Default: `No numeric threshold for {{.metric}} in {{.field}} — specify a measurable target.`,
		PM:      `{{.field}} sets a goal for {{.metric}} without a number — we can't tell when it's met.`,
		QA:      `{{.metric}} in {{.field}} has no threshold — add a target (e.g. p95 < X ms) so a pass/fail assertion exists.`,
		Dev:     `{{.metric}} in {{.field}} has no numeric bound. Pin a threshold at the assertion site so drift is caught.`,
	},
	"incomplete.no_given_when_then": {
		Params:  []string{"field"},
		Default: `Acceptance criteria in {{.field}} lack Given/When/Then structure.`,
		PM:      `{{.field}} isn't structured — Given/When/Then would make scope boundaries obvious to the team estimating it.`,
		QA:      `{{.field}} lacks Given/When/Then. Restructure so each scenario maps cleanly to a test.`,
		Dev:     `{{.field}} is narrative rather than scenario-shaped — split into Given/When/Then so the implementation has a 1:1 target.`,
	},
	"incomplete.no_rollback": {
		Params:  []string{"field"},
		Default: `Rollback behavior undefined in {{.field}}.`,
		PM:      `{{.field}} doesn't describe the undo / rollback path — operations will inherit the ambiguity when something goes wrong in prod.`,
		QA:      `{{.field}} has no rollback scenario. Add one covering failure midway through the operation.`,
		Dev:     `{{.field}} skips the rollback / compensating action. Decide on transaction boundaries or idempotency and document them.`,
	},
}
