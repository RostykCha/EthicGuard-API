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

// Client wraps the Anthropic SDK for EthicGuard's analysis calls.
type Client struct {
	inner anthropic.Client
	model string
}

// New creates an LLM client for the given model.
func New(apiKey, model string) *Client {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &Client{inner: client, model: model}
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

	msg, err := c.inner.Messages.New(ctx, anthropic.MessageNewParams{
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
	var sb strings.Builder
	for _, block := range msg.Content {
		if v, ok := block.AsAny().(anthropic.TextBlock); ok {
			sb.WriteString(v.Text)
		}
	}
	return sb.String(), nil
}
