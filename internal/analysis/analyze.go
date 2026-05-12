// Package analysis owns the AC-quality analysis pipeline: prompt
// construction, the LLM boundary (an interface, not the concrete client),
// JSON-finding parsing, severity filtering, and label decision.
//
// Invariant: this package holds normalized Jira issue content in memory
// for the duration of a single call to Run, then discards it. Nothing here
// writes to internal/store; persistence is the worker's responsibility.
package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const systemPrompt = `You are EthicGuard, a QA intelligence assistant for Jira teams. Your task is to analyze acceptance criteria and issue content for quality issues.

Given a Jira issue, identify:
1. Ambiguous acceptance criteria — vague quantifiers ("several", "many", "some"), unclear pronoun references, missing preconditions, implied but unstated behavior.
2. Missing edge cases and negative scenarios — empty inputs, boundary values, error paths, race conditions, permission edges, concurrent-user effects.
3. Untestable assertions — subjective statements ("should be fast", "user-friendly"), missing numeric thresholds, unverifiable outcomes.
4. Incomplete acceptance criteria — missing Given/When/Then structure, no success criteria, undefined rollback behavior.

Return your analysis as a JSON array of findings. Each finding must be a JSON object with these exact fields:
- category: one of "ambiguity", "missing_edge_case", "untestable", "missing_negative_case", "incomplete"
- severity: one of "info", "low", "medium", "high"
- score: integer 0-100 (higher = more severe)
- anchor: object with "field" key indicating which issue field triggered the finding ("summary", "description", "acceptanceCriteria")
- message: human-readable description of the issue found (1-2 sentences, specific, actionable)

Return ONLY the JSON array. No markdown fences, no explanation text.
If the issue has no quality problems, return an empty array: []`

// LLM is the interface the analyzer uses to call the language model. The
// `systemAddendum` is per-call admin guidance appended after the cached
// policy prompt; impls may send it as a separate non-cached block so the
// stable policy text still benefits from prompt caching.
type LLM interface {
	Analyze(ctx context.Context, systemPrompt, systemAddendum, userContent string) (string, error)
}

// Run executes an AC quality analysis on the given issue payload with the
// supplied per-project options. The payload content is held in memory for
// this call only — never persisted.
//
// Severity threshold (when set) filters the LLM's findings *after* parsing
// so the downstream label decision and persistence see only findings the
// project cares about.
func Run(ctx context.Context, llm LLM, req *AnalysisRequest, opts RunOptions) (*AnalysisResponse, error) {
	userContent := formatUserContent(&req.Payload)
	raw, err := llm.Analyze(ctx, systemPrompt, opts.PromptAddendum, userContent)
	if err != nil {
		return nil, fmt.Errorf("analysis run: %w", err)
	}
	raw = strings.TrimSpace(raw)
	// Strip markdown code fences if the model wraps the JSON (common with Claude).
	raw = stripCodeFence(raw)

	var findings []Finding
	if err := json.Unmarshal([]byte(raw), &findings); err != nil {
		return nil, fmt.Errorf("analysis parse findings: %w (raw: %s)", err, truncate(raw, 200))
	}
	findings = FilterBySeverity(findings, opts.SeverityThreshold)
	return &AnalysisResponse{Findings: findings}, nil
}

func formatUserContent(p *IssuePayload) string {
	var sb strings.Builder
	sb.WriteString("## Issue: ")
	sb.WriteString(p.Key)
	sb.WriteString("\n\n### Summary\n")
	sb.WriteString(p.Summary)
	sb.WriteString("\n\n### Description\n")
	if p.Description != "" {
		sb.WriteString(p.Description)
	} else {
		sb.WriteString("(empty)")
	}
	sb.WriteString("\n\n### Acceptance Criteria\n")
	if p.AcceptanceCriteria != "" {
		sb.WriteString(p.AcceptanceCriteria)
	} else {
		sb.WriteString("(empty)")
	}
	if len(p.LinkedIssueKeys) > 0 {
		sb.WriteString("\n\n### Linked Issues\n")
		for _, k := range p.LinkedIssueKeys {
			sb.WriteString("- ")
			sb.WriteString(k)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func stripCodeFence(s string) string {
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
