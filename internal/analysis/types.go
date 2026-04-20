package analysis

// Finding is a single QA issue detected in an issue's content. The LLM emits
// one of these per problem it finds. Zero-retention rule: Findings that cross
// into internal/store must carry only MessageKey + Params + Anchor +
// RationaleTag, never a free-text description of the issue. Human text is
// assembled from the catalog at response time.
type Finding struct {
	Category     string            `json:"category"`
	Severity     string            `json:"severity"`
	Score        int               `json:"score"`
	Anchor       Anchor            `json:"anchor"`
	MessageKey   string            `json:"messageKey"`
	Params       map[string]string `json:"params,omitempty"`
	RationaleTag string            `json:"rationaleTag,omitempty"`
}

// Anchor points into a specific region of a Jira field. Start/End are UTF-8
// byte offsets into the field's plain-text representation (stringified ADF).
// Zero offsets mean "whole field, no sub-span."
type Anchor struct {
	Field string `json:"field"`
	Start int    `json:"start,omitempty"`
	End   int    `json:"end,omitempty"`
}

// AnalysisRequest is what the Forge app POSTs to /v1/analysis.
//
// Escalate, when true, signals the worker to use the heavy model
// (claude-opus-4-6) for this specific run. Costs more; reserved for explicit
// user action ("Go deeper").
type AnalysisRequest struct {
	IssueKey   string       `json:"issueKey"`
	ProjectKey string       `json:"projectKey"`
	Kind       string       `json:"kind"`
	Escalate   bool         `json:"escalate,omitempty"`
	Payload    IssuePayload `json:"payload"`
}

// IssuePayload is the normalized Jira content from the Forge UI. Lives in
// memory for one analysis call; never written to Postgres.
type IssuePayload struct {
	Key                string   `json:"key"`
	Summary            string   `json:"summary"`
	Description        string   `json:"description"`
	AcceptanceCriteria string   `json:"acceptanceCriteria"`
	LinkedIssueKeys    []string `json:"linkedIssueKeys"`
}
