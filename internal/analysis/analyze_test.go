package analysis

import (
	"context"
	"strings"
	"testing"
)

type stubLLM struct {
	lastSystemPrompt   string
	lastSystemAddendum string
	lastUserContent    string
	response           string
	err                error
}

func (s *stubLLM) Analyze(_ context.Context, systemPrompt, systemAddendum, userContent string) (string, error) {
	s.lastSystemPrompt = systemPrompt
	s.lastSystemAddendum = systemAddendum
	s.lastUserContent = userContent
	if s.err != nil {
		return "", s.err
	}
	return s.response, nil
}

func TestRun_FiltersBelowThreshold(t *testing.T) {
	llm := &stubLLM{
		response: `[
			{"category":"ambiguity","severity":"low","score":20,"anchor":{"field":"acceptanceCriteria"},"message":"x"},
			{"category":"ambiguity","severity":"medium","score":50,"anchor":{"field":"acceptanceCriteria"},"message":"y"},
			{"category":"ambiguity","severity":"high","score":80,"anchor":{"field":"acceptanceCriteria"},"message":"z"}
		]`,
	}
	req := &AnalysisRequest{Payload: IssuePayload{Key: "KAN-1"}}
	resp, err := Run(context.Background(), llm, req, RunOptions{SeverityThreshold: SeverityMedium})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(resp.Findings) != 2 {
		t.Fatalf("findings = %d, want 2 (low dropped)", len(resp.Findings))
	}
	for _, f := range resp.Findings {
		if f.Severity == SeverityLow || f.Severity == SeverityInfo {
			t.Errorf("finding %+v should have been filtered out", f)
		}
	}
}

func TestRun_PassesAddendumToLLM(t *testing.T) {
	llm := &stubLLM{response: "[]"}
	req := &AnalysisRequest{Payload: IssuePayload{Key: "KAN-1"}}
	_, err := Run(context.Background(), llm, req, RunOptions{
		PromptAddendum: "Focus on accessibility criteria.",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(llm.lastSystemAddendum, "accessibility") {
		t.Errorf("addendum not forwarded to LLM; got %q", llm.lastSystemAddendum)
	}
	if !strings.Contains(llm.lastSystemPrompt, "EthicGuard") {
		t.Errorf("static system prompt missing or altered")
	}
}

func TestRun_HandlesEmptyOptions(t *testing.T) {
	llm := &stubLLM{
		response: `[{"category":"ambiguity","severity":"info","score":10,"anchor":{"field":"summary"},"message":"x"}]`,
	}
	req := &AnalysisRequest{Payload: IssuePayload{Key: "KAN-1"}}
	resp, err := Run(context.Background(), llm, req, RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(resp.Findings) != 1 {
		t.Errorf("findings = %d, want 1 (no threshold)", len(resp.Findings))
	}
	if llm.lastSystemAddendum != "" {
		t.Errorf("addendum should be empty when not set; got %q", llm.lastSystemAddendum)
	}
}
