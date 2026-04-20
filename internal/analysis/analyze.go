package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// systemPrompt instructs Claude to emit findings against a *fixed* enum of
// message keys. The API's catalog holds the human text for each key; the LLM
// never writes free-form narration. This is the backbone of zero-retention
// compliance for persisted findings.
const systemPrompt = `You are EthicGuard, a QA intelligence assistant for Jira teams. Analyze acceptance criteria and issue content for quality issues and report them as structured findings.

You MUST return a JSON array of findings. Each finding is an object with these exact fields:
- category: one of "ambiguity" | "missing_edge_case" | "untestable" | "missing_negative_case" | "incomplete"
- severity: one of "info" | "low" | "medium" | "high"
- score: integer 0-100 (higher = more severe)
- anchor: object { "field": "summary" | "description" | "acceptanceCriteria", "start"?: int, "end"?: int }
- messageKey: one of the fixed keys below
- params: object mapping placeholder name to value, where every value is either
  a short identifier (field name, enum value, count) or a non-negative integer
  rendered as a string. You MUST NOT place user-authored text, quoted phrases,
  or sentences from the issue into params. Param values must match
  ^[A-Za-z0-9_.:/-]{0,48}$ when they are strings.
- rationaleTag (optional): label WHY this finding is non-obvious, one of:
    "assumption_gap"     — the AC assumes unstated state or prior knowledge
    "spec_conflict"      — contradicts something earlier in the issue or a linked one
    "missing_negative"   — the positive path is covered but the negative/failure path isn't
    "unstated_behavior"  — implied behavior that a careful reader might miss
    "weak_precondition"  — the trigger condition is too loose to be testable
  Omit this field when the finding is a straightforward lint (e.g. vague
  quantifier) that doesn't need a depth-signal. Use it sparingly — only
  when a human reviewer might miss the finding.

Allowed messageKey values and their required params:

- "ambiguity.vague_quantifier"         params: { "field": string, "term": string }
    Used for vague quantifiers like "several", "many", "some", "a few".
    term is the offending word, copied verbatim when it is a single token from
    a fixed list; otherwise omit.
- "ambiguity.unclear_pronoun"          params: { "field": string }
- "ambiguity.missing_precondition"     params: { "field": string }
- "missing_edge_case.empty_input"      params: { "field": string }
- "missing_edge_case.boundary"         params: { "field": string }
- "missing_edge_case.concurrency"      params: { "field": string }
- "missing_negative_case.permission"   params: { "field": string }
- "missing_negative_case.error_path"   params: { "field": string }
- "untestable.subjective"              params: { "field": string }
- "untestable.missing_threshold"       params: { "field": string, "metric": string }
- "incomplete.no_given_when_then"      params: { "field": string }
- "incomplete.no_rollback"             params: { "field": string }

If the issue has no quality problems, return [].

Return ONLY the JSON array. No markdown fences, no explanation.`

// LLM is the minimal surface analyze.Run needs from the Anthropic client.
type LLM interface {
	Analyze(ctx context.Context, systemPrompt, userContent string) (string, error)
}

// Run executes an AC quality analysis on the given issue payload. The payload
// content is held in memory for this call only — never persisted.
func Run(ctx context.Context, llm LLM, req *AnalysisRequest) ([]Finding, error) {
	userContent := formatUserContent(&req.Payload)
	raw, err := llm.Analyze(ctx, systemPrompt, userContent)
	if err != nil {
		return nil, fmt.Errorf("analysis run: %w", err)
	}
	raw = stripCodeFence(strings.TrimSpace(raw))

	var findings []Finding
	if err := json.Unmarshal([]byte(raw), &findings); err != nil {
		return nil, fmt.Errorf("analysis parse findings: %w (raw: %s)", err, truncate(raw, 200))
	}
	return findings, nil
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
