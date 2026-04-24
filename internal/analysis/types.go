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
	Summary            string   `json:"summary"`
	Description        string   `json:"description"`
	AcceptanceCriteria string   `json:"acceptanceCriteria"`
	LinkedIssueKeys    []string `json:"linkedIssueKeys"`
}

// AnalysisResponse is the shape returned to the Forge UI.
type AnalysisResponse struct {
	Findings []Finding `json:"findings"`
}
