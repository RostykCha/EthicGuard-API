package httpapi

// Validation limits and small validators shared by handlers. Per the
// convention in CLAUDE.md ("Code conventions"), validation constants live
// here rather than inline in handler files so a fresh reader has one place
// to look for the bounds of accepted inputs.

const (
	// maxIssueTypes caps how many issue types an admin can list as in-scope
	// for a single project. A team-managed Jira project rarely exceeds a
	// dozen; 50 is comfortably above that without inviting abuse.
	maxIssueTypes = 50

	// maxIssueTypeIDLen bounds the length of each issue-type id string.
	// Jira issue type ids are short numeric strings; this is a guard rail.
	maxIssueTypeIDLen = 64

	// maxPromptAddendumBytes caps the per-project prompt addendum the
	// admin can supply. The addendum is forwarded into the analysis system
	// prompt; bounded so a single project can't inflate every LLM call.
	maxPromptAddendumBytes = 1024

	// defaultSeverityThreshold is what a brand-new project gets before the
	// admin saves anything. Matches the migration default.
	defaultSeverityThreshold = "medium"
)

// isValidSeverity reports whether s is one of the four severity tokens the
// API accepts. The canonical list also lives as constants in
// internal/analysis/labels.go (SeverityHigh / Medium / Low / Info) — keep
// them in lock-step.
func isValidSeverity(s string) bool {
	switch s {
	case "info", "low", "medium", "high":
		return true
	}
	return false
}
