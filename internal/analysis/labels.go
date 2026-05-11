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
const (
	SeverityHigh   = "high"
	SeverityMedium = "medium"
	SeverityLow    = "low"
	SeverityInfo   = "info"
)

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
