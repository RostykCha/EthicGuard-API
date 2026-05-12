package analysis

import "strings"

// LabelDecision is the set of labels EthicGuard writes back onto a Jira issue
// after analysis. These are stable enum strings (exact spelling matters — the
// workflow validator and the issue panel both string-match them).
const (
	LabelACVerified = "AC verified"
	LabelACDefect   = "AC defect"
	LabelACNotReady = "AC not ready"
	LabelNoTest     = "no test"
)

// MinACLength is the bar below which AC text is treated as "not ready" even
// if the LLM didn't surface a finding. Stops the verified label from leaking
// onto effectively empty AC like a single hyphen or "tbd".
const MinACLength = 20

// Severity levels (mirrors what the LLM emits and the schema constrains).
// Kept as untyped string constants so wire types (Finding.Severity, store
// columns, test fixtures) interoperate without conversion. The typed
// Severity below exists only for the rank table.
const (
	SeverityHigh   = "high"
	SeverityMedium = "medium"
	SeverityLow    = "low"
	SeverityInfo   = "info"
)

// Severity is a typed view used for ordering comparisons. Construct from a
// string at the boundary; never re-type the wire fields.
type Severity string

// severityRanks is the canonical ordering. Unknown values rank 0 so they're
// dropped by any non-empty threshold — keeps a future typo in the model
// output from silently slipping past a "medium" gate.
var severityRanks = map[Severity]int{
	Severity(SeverityHigh):   4,
	Severity(SeverityMedium): 3,
	Severity(SeverityLow):    2,
	Severity(SeverityInfo):   1,
}

// Rank returns the integer rank for s. Returns 0 for any token outside the
// canonical four — the caller decides what that means.
func (s Severity) Rank() int {
	return severityRanks[s]
}

// AtLeast reports whether s meets or exceeds the threshold. Unknown s or
// threshold values are treated as "below everything" — see the test cases
// in labels_test.go for the exact semantics.
func (s Severity) AtLeast(threshold Severity) bool {
	r := s.Rank()
	t := threshold.Rank()
	if r == 0 || t == 0 {
		return false
	}
	return r >= t
}

// FilterBySeverity returns the subset of findings whose severity is at least
// the given threshold. Empty or unknown threshold passes everything through
// unchanged — matches the project-config default ("medium" applies; "" or
// "critical" means "no filter").
func FilterBySeverity(in []Finding, threshold string) []Finding {
	if threshold == "" {
		return in
	}
	t := Severity(threshold)
	if t.Rank() == 0 {
		return in
	}
	out := make([]Finding, 0, len(in))
	for _, f := range in {
		if Severity(f.Severity).AtLeast(t) {
			out = append(out, f)
		}
	}
	return out
}

// Decision is the full outcome of an analysis: the primary AC label plus
// the orthogonal "no test" flag. Callers (worker, agent) translate this
// into the set of labels to write to Jira.
type Decision struct {
	Primary string // one of LabelACVerified / LabelACDefect / LabelACNotReady
	NoTest  bool   // true when the issue has no test links
}

// Labels flattens the decision into the label list to write to Jira.
func (d Decision) Labels() []string {
	labels := []string{d.Primary}
	if d.NoTest {
		labels = append(labels, LabelNoTest)
	}
	return labels
}

// Decide is the pure rule for mapping an analysis result to an AC label.
//
//   - empty / very short AC                          → AC not ready
//   - any high-severity finding                      → AC defect
//   - otherwise                                      → AC verified
//
// Orthogonal: no test links anywhere on the issue   → also stamps `no test`.
//
// The rule is intentionally simple and inspectable; it lives next to the
// constants it returns so future tweaks land in one place.
func Decide(findings []Finding, payload *IssuePayload) Decision {
	d := Decision{NoTest: !payload.HasTestLinks}

	ac := strings.TrimSpace(payload.AcceptanceCriteria)
	if len(ac) < MinACLength {
		d.Primary = LabelACNotReady
		return d
	}
	for _, f := range findings {
		if f.Severity == SeverityHigh {
			d.Primary = LabelACDefect
			return d
		}
	}
	d.Primary = LabelACVerified
	return d
}
