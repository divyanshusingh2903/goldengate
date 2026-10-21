package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Anthropic calls Claude through the official SDK.
type Anthropic struct {
	client anthropic.Client
	model  string
}

// NewAnthropic builds a client. An empty apiKey lets the SDK find credentials
// itself (ANTHROPIC_API_KEY, or a profile from `ant auth login`).
func NewAnthropic(apiKey, model string, opts ...option.RequestOption) *Anthropic {
	if model == "" {
		model = DefaultAnthropicModel
	}
	all := []option.RequestOption{option.WithRequestTimeout(90 * time.Second)}
	if apiKey != "" {
		all = append(all, option.WithAPIKey(apiKey))
	}
	return &Anthropic{client: anthropic.NewClient(append(all, opts...)...), model: model}
}

// Name implements Client.
func (a *Anthropic) Name() string { return ProviderAnthropic + ":" + a.model }

// JSON implements Client with structured outputs, so the reply always parses
// against schema.
func (a *Anthropic) JSON(ctx context.Context, system, user string, schema map[string]any) ([]byte, error) {
	resp, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: MaxOutputTokens,
		// no thinking: a summary needs none, and it would eat the answer's room
		// (Haiku 5.5 allows turning it off at low effort)
		Thinking: anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}},
		System:   []anthropic.TextBlockParam{{Text: system}},
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(user))},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffortLow, // a summary needs little reasoning
			Format: anthropic.JSONOutputFormatParam{Schema: schema},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("llm: anthropic: %w", err)
	}
	switch resp.StopReason {
	case anthropic.StopReasonRefusal:
		return nil, ErrRefused
	case anthropic.StopReasonMaxTokens:
		return nil, fmt.Errorf("llm: anthropic: the answer was cut off at max_tokens")
	}
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			return []byte(t.Text), nil
		}
	}
	return nil, fmt.Errorf("llm: anthropic: no text in the answer")
}

// Models implements Lister.
func (a *Anthropic) Models(ctx context.Context) ([]string, error) {
	page, err := a.client.Models.List(ctx, anthropic.ModelListParams{Limit: anthropic.Int(100)})
	if err != nil {
		return nil, fmt.Errorf("llm: anthropic: %w", err)
	}
	var out []string
	for _, m := range page.Data {
		out = append(out, m.ID)
	}
	return out, nil
}
