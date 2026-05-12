package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// fakeSender captures the params handed to New so tests can assert on the
// request shape, and returns a scripted response or error.
type fakeSender struct {
	gotParams anthropic.MessageNewParams
	callCount int
	response  *anthropic.Message
	err       error
}

func (f *fakeSender) New(_ context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	f.gotParams = params
	f.callCount++
	if f.err != nil {
		return nil, f.err
	}
	return f.response, nil
}

// makeMessage builds an anthropic.Message with the given text blocks.
// Constructed via the SDK's helper so we get the correct union shape.
func makeMessage(texts ...string) *anthropic.Message {
	blocks := make([]anthropic.ContentBlockUnion, 0, len(texts))
	for _, t := range texts {
		blocks = append(blocks, anthropic.ContentBlockUnion{
			Type: "text",
			Text: t,
		})
	}
	return &anthropic.Message{Content: blocks}
}

func TestAnalyze_HappyPath(t *testing.T) {
	fake := &fakeSender{response: makeMessage("hello world")}
	c := newClientWithSender(fake, "claude-sonnet-4-6")

	got, err := c.Analyze(context.Background(), "sys prompt", "", "user content")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got != "hello world" {
		t.Errorf("text = %q, want %q", got, "hello world")
	}
	if fake.callCount != 1 {
		t.Errorf("call count = %d, want 1", fake.callCount)
	}
	if got := string(fake.gotParams.Model); got != "claude-sonnet-4-6" {
		t.Errorf("model = %q, want claude-sonnet-4-6", got)
	}
	if fake.gotParams.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096", fake.gotParams.MaxTokens)
	}
}

func TestAnalyze_SystemPromptCached(t *testing.T) {
	fake := &fakeSender{response: makeMessage("ok")}
	c := newClientWithSender(fake, "m")

	_, _ = c.Analyze(context.Background(), "the system prompt", "", "u")

	if len(fake.gotParams.System) != 1 {
		t.Fatalf("system blocks = %d, want 1 (no addendum)", len(fake.gotParams.System))
	}
	first := fake.gotParams.System[0]
	if first.Text != "the system prompt" {
		t.Errorf("first system block text = %q, want %q", first.Text, "the system prompt")
	}
	if first.CacheControl.Type != "ephemeral" {
		t.Errorf("first system block cache control = %q, want ephemeral",
			first.CacheControl.Type)
	}
}

func TestAnalyze_AddendumAddsSecondBlock(t *testing.T) {
	fake := &fakeSender{response: makeMessage("ok")}
	c := newClientWithSender(fake, "m")

	_, _ = c.Analyze(
		context.Background(),
		"main prompt",
		"per-project addendum",
		"user",
	)

	if len(fake.gotParams.System) != 2 {
		t.Fatalf("system blocks = %d, want 2 (addendum present)", len(fake.gotParams.System))
	}
	if fake.gotParams.System[0].Text != "main prompt" {
		t.Errorf("first block text = %q", fake.gotParams.System[0].Text)
	}
	if fake.gotParams.System[1].Text != "per-project addendum" {
		t.Errorf("second block text = %q", fake.gotParams.System[1].Text)
	}
	// The addendum block must NOT carry the cache control — otherwise we
	// invalidate the prompt cache on every per-project change.
	if fake.gotParams.System[1].CacheControl.Type != "" {
		t.Errorf("addendum block has cache control %q; should be empty",
			fake.gotParams.System[1].CacheControl.Type)
	}
}

func TestAnalyze_WhitespaceOnlyAddendumIgnored(t *testing.T) {
	fake := &fakeSender{response: makeMessage("ok")}
	c := newClientWithSender(fake, "m")

	_, _ = c.Analyze(context.Background(), "p", "   \n\t  ", "u")

	if len(fake.gotParams.System) != 1 {
		t.Errorf("whitespace-only addendum should be ignored; got %d blocks", len(fake.gotParams.System))
	}
}

func TestAnalyze_MultipleContentBlocksConcatenated(t *testing.T) {
	fake := &fakeSender{response: makeMessage("part one ", "part two")}
	c := newClientWithSender(fake, "m")

	got, err := c.Analyze(context.Background(), "p", "", "u")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got != "part one part two" {
		t.Errorf("text = %q, want concatenation", got)
	}
}

func TestAnalyze_EmptyResponse(t *testing.T) {
	fake := &fakeSender{response: makeMessage()}
	c := newClientWithSender(fake, "m")

	got, err := c.Analyze(context.Background(), "p", "", "u")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got != "" {
		t.Errorf("text = %q, want empty", got)
	}
}

func TestAnalyze_SDKErrorWrapped(t *testing.T) {
	sentinel := errors.New("network blew up")
	fake := &fakeSender{err: sentinel}
	c := newClientWithSender(fake, "m")

	_, err := c.Analyze(context.Background(), "p", "", "u")
	if err == nil {
		t.Fatal("expected error from SDK")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error chain doesn't include sentinel: %v", err)
	}
}

func TestNew_BuildsRealClient(t *testing.T) {
	// Smoke test: New() should not panic and should produce a usable Client
	// with the model field set. We can't exercise the real SDK without a
	// network call, so we stop at constructor sanity.
	c := New("test-key", "claude-test-1")
	if c == nil {
		t.Fatal("New returned nil")
	}
	if c.model != "claude-test-1" {
		t.Errorf("model = %q, want claude-test-1", c.model)
	}
	if c.sender == nil {
		t.Error("sender should be initialized")
	}
}
