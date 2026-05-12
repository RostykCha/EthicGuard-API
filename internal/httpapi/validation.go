package httpapi

import (
	"errors"
	"fmt"
)

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

	// maxFindingsPerResult caps how many findings a single externally-
	// computed verdict can carry. Rovo's typical analysis produces a
	// handful; 50 is comfortably above realistic upper bound.
	maxFindingsPerResult = 50

	// maxStringFieldLen bounds short string fields on the verdict payload
	// (category, severity, messageKey, label, kind, issueKey, projectKey).
	maxStringFieldLen = 128
)

// validResultLabels is the closed set of label tokens accepted by the
// /v1/analysis/results endpoint. Must match the LABEL_* constants in
// EthicGuard-UI/src/lib/acLabels.ts — Jira rejects whitespace in labels,
// hence the hyphenated form.
var validResultLabels = map[string]struct{}{
	"AC-verified":  {},
	"AC-defect":    {},
	"AC-not-ready": {},
	"no-test":      {},
}

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

// validateAnalysisResultsRequest enforces the wire-shape rules for the
// /v1/analysis/results endpoint. The single bright line: no finding may
// carry a free-text `message` field. That's the zero-retention boundary —
// findings persist as a stable message_key only; the human text is rendered
// from the static catalog at read time.
func validateAnalysisResultsRequest(req *analysisResultsRequest) error {
	if req == nil {
		return errors.New("missing request body")
	}
	if req.IssueKey == "" {
		return errors.New("issueKey is required")
	}
	if req.ProjectKey == "" {
		return errors.New("projectKey is required")
	}
	if req.Kind == "" {
		return errors.New("kind is required")
	}
	if len(req.IssueKey) > maxStringFieldLen ||
		len(req.ProjectKey) > maxStringFieldLen ||
		len(req.Kind) > maxStringFieldLen {
		return errors.New("issueKey, projectKey, and kind must be shorter than 128 bytes")
	}
	if _, ok := validResultLabels[req.Label]; !ok {
		return fmt.Errorf("invalid label %q (must be AC-verified, AC-defect, AC-not-ready, or no-test)", req.Label)
	}
	if len(req.Findings) > maxFindingsPerResult {
		return fmt.Errorf("too many findings (%d); cap is %d", len(req.Findings), maxFindingsPerResult)
	}
	for i, f := range req.Findings {
		if f.Message != "" {
			// Zero-retention boundary. The message_key is the only text we
			// store; the human-readable text comes from the static catalog
			// at render time. A `message` field on the wire is a bug in the
			// caller — reject loudly rather than silently dropping it.
			return fmt.Errorf("finding[%d]: free-text 'message' field is forbidden (zero-retention)", i)
		}
		if f.Category == "" || f.Severity == "" || f.MessageKey == "" {
			return fmt.Errorf("finding[%d]: category, severity, and messageKey are required", i)
		}
		if !isValidSeverity(f.Severity) {
			return fmt.Errorf("finding[%d]: invalid severity %q", i, f.Severity)
		}
		if len(f.Category) > maxStringFieldLen ||
			len(f.MessageKey) > maxStringFieldLen {
			return fmt.Errorf("finding[%d]: category and messageKey must be shorter than 128 bytes", i)
		}
	}
	return nil
}
