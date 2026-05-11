package analysis

// MessageKey derives the stable storage key for a finding from its category
// and severity. The key is what goes into Postgres (`findings.message_key`)
// — the LLM's free-text message is held only in memory per the zero-retention
// rule, so we render through the catalog at read time instead.
func MessageKey(category, severity string) string {
	return category + "." + severity
}

// ResolveMessage maps a stored message_key back to a human-readable string
// for UI rendering. Unknown keys fall back to a generic message rather than
// surfacing the raw key.
func ResolveMessage(key string) string {
	if m, ok := messageCatalog[key]; ok {
		return m
	}
	return "Quality concern detected — review the acceptance criteria for clarity and completeness."
}

// messageCatalog is the static lookup from message_key → display text. Keep
// the strings generic; they describe the *kind* of issue, not the issue's
// content. Adding a new entry here is the right way to evolve UI copy
// without storing customer text in the database.
var messageCatalog = map[string]string{
	// ambiguity
	"ambiguity.high":   "Acceptance criteria contain ambiguous quantifiers or unclear references — rewrite with concrete, testable terms.",
	"ambiguity.medium": "Acceptance criteria are partially ambiguous — consider tightening vague language.",
	"ambiguity.low":    "Minor ambiguity in acceptance criteria — review for clarity.",
	"ambiguity.info":   "Acceptance criteria readability could be improved.",

	// missing_edge_case
	"missing_edge_case.high":   "Critical edge case missing — add explicit handling for empty / boundary / error inputs.",
	"missing_edge_case.medium": "Edge case not covered — define expected behavior at boundaries.",
	"missing_edge_case.low":    "Minor edge case not addressed.",
	"missing_edge_case.info":   "Consider whether edge cases need explicit acceptance criteria.",

	// missing_negative_case
	"missing_negative_case.high":   "Negative / failure path is undefined — specify behavior when the operation fails.",
	"missing_negative_case.medium": "Failure scenarios partially covered — add criteria for error states.",
	"missing_negative_case.low":    "Minor negative-case gap.",
	"missing_negative_case.info":   "Consider explicit negative-case criteria.",

	// untestable
	"untestable.high":   "Acceptance criterion is not objectively testable — replace subjective terms with measurable thresholds.",
	"untestable.medium": "Acceptance criterion is hard to verify — add a clear pass/fail condition.",
	"untestable.low":    "Acceptance criterion could be more testable.",
	"untestable.info":   "Consider whether the criteria are observable to a tester.",

	// incomplete
	"incomplete.high":   "Acceptance criteria are missing structure (e.g., Given/When/Then) and a clear success condition.",
	"incomplete.medium": "Acceptance criteria are missing parts of the expected behavior.",
	"incomplete.low":    "Minor gap in acceptance-criteria coverage.",
	"incomplete.info":   "Acceptance criteria could be more complete.",
}
