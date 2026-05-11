package analysis

// Finding is a single QA issue detected in an issue's content. Returned from
// the LLM and rendered in the Forge UI. Zero-retention note: findings are
// returned over the wire but never stored with the human-readable message —
// only message_key + anchor go to Postgres (once persistence lands).
type Finding struct {
	Category string `json:"category"`
	Severity string `json:"severity"`
	Score    int    `json:"score"`
	Anchor   Anchor `json:"anchor"`
	Message  string `json:"message"`
}

type Anchor struct {
	Field string `json:"field"`
}

// AnalysisRequest is what the handler sends into the analysis pipeline.
type AnalysisRequest struct {
	IssueKey   string       `json:"issueKey"`
	ProjectKey string       `json:"projectKey"`
	Kind       string       `json:"kind"`
	Payload    IssuePayload `json:"payload"`
}

// IssuePayload is the normalized Jira content from the Forge UI. This lives
// only in memory for the duration of the analysis call — never persisted.
type IssuePayload struct {
	Key                string   `json:"key"`
	IssueTypeID        string   `json:"issueTypeId"`
	Summary            string   `json:"summary"`
	Description        string   `json:"description"`
	AcceptanceCriteria string   `json:"acceptanceCriteria"`
	LinkedIssueKeys    []string `json:"linkedIssueKeys"`
	HasTestLinks       bool     `json:"hasTestLinks"`
}

// AnalysisResponse is the shape returned to the Forge UI.
type AnalysisResponse struct {
	Findings []Finding `json:"findings"`
}

// RunOptions carries per-project knobs the worker passes into analysis.Run.
// These are derived from the project's stored ProjectConfig at enqueue time
// and stashed in the in-memory job bus alongside the payload, so the worker
// doesn't need a store dependency.
//
// SeverityThreshold drops any finding strictly below this rank before the
// label is decided (e.g. threshold "medium" → findings with severity "info"
// and "low" are filtered out). Empty string means "use the default behaviour"
// — keep every finding the model returned.
//
// PromptAddendum is appended to the analysis system prompt as an extra,
// non-cached system block. Empty string means "no addendum, plain system
// prompt only" (which preserves the prompt-cache hit).
type RunOptions struct {
	SeverityThreshold string
	PromptAddendum    string
}
