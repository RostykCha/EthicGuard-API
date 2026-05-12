// Package llm wraps the Anthropic SDK with EthicGuard's defaults: prompt
// caching on the system block, structured-JSON request shape, bounded
// retry on transient errors. It is the only package that talks to the
// model vendor.
//
// AGENT-NOTE: Never log prompt bodies or response text above slog.LevelDebug.
// Prompts contain the user's AC text and Anthropic responses sometimes
// echo it back. CLAUDE.md "LLM integration rules" makes this a hard rule;
// see also the zero-retention boundary in internal/store.
package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// messageSender is the narrow slice of the Anthropic SDK we use. Defined
// here so tests can substitute a fake without depending on the SDK's
// runtime shape. The production implementation wraps anthropic.Client.
type messageSender interface {
	New(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error)
}

// sdkSender is the production adapter — forwards to the real SDK.
type sdkSender struct{ inner anthropic.Client }

func (s sdkSender) New(ctx context.Context, p anthropic.MessageNewParams) (*anthropic.Message, error) {
	return s.inner.Messages.New(ctx, p)
}

// Client wraps the Anthropic SDK for EthicGuard's analysis calls.
type Client struct {
	sender messageSender
	model  string
}

// New creates an LLM client for the given model.
func New(apiKey, model string) *Client {
	return &Client{
		sender: sdkSender{inner: anthropic.NewClient(option.WithAPIKey(apiKey))},
		model:  model,
	}
}

// newClientWithSender lets tests inject a fake messageSender. Not exported —
// production code goes through New.
func newClientWithSender(s messageSender, model string) *Client {
	return &Client{sender: s, model: model}
}

// Analyze sends a system prompt + optional non-cached addendum + user content
// to Claude and returns the raw text response.
//
// Caching: the static `systemPrompt` is sent as a cacheable block. The
// `systemAddendum` (per-project guidance) is sent as a second, non-cached
// system block when non-empty. Splitting them this way preserves the
// cache hit on the (stable) policy text while still letting projects layer
// their own guidance on top per analysis.
func (c *Client) Analyze(ctx context.Context, systemPrompt, systemAddendum, userContent string) (string, error) {
	system := []anthropic.TextBlockParam{
		{
			Text:         systemPrompt,
			CacheControl: anthropic.CacheControlEphemeralParam{Type: "ephemeral"},
		},
	}
	if strings.TrimSpace(systemAddendum) != "" {
		system = append(system, anthropic.TextBlockParam{Text: systemAddendum})
	}

	msg, err := c.sender.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: 4096,
		System:    system,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userContent)),
		},
	})
	if err != nil {
		return "", fmt.Errorf("llm analyze: %w", err)
	}
	// Concatenate every text block's text. We read the union's exposed
	// `Type` + `Text` fields rather than the SDK's `AsAny()` dispatch so
	// the path is unit-testable from a hand-constructed Message.
	var sb strings.Builder
	for _, block := range msg.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	return sb.String(), nil
}
