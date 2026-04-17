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

// Analyze sends a system prompt + user content to Claude and returns the raw
// text response. Prompt caching is enabled on the system prompt via cache
// control; repeated calls with the same system prompt (which includes the QA
// policy) reuse the cached prefix.
func (c *Client) Analyze(ctx context.Context, systemPrompt, userContent string) (string, error) {
	msg, err := c.inner.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{
			{
				Text:         systemPrompt,
				CacheControl: anthropic.CacheControlEphemeralParam{Type: "ephemeral"},
			},
		},
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
