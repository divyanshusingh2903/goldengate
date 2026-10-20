// Package llm talks to the language models the optional summarizer can use.
// It knows nothing about sessions: it sends a system prompt and one user
// message and gets back a JSON object that matches a schema. What text may be
// sent, and how it is redacted first, is the summarize package's job.
package llm

import (
	"context"
	"errors"
)

// Client asks a model for one JSON object matching schema.
type Client interface {
	// Name identifies provider and model, e.g. "anthropic:claude-haiku-5-5".
	// It is recorded in each payload as summarized_by.
	Name() string
	JSON(ctx context.Context, system, user string, schema map[string]any) ([]byte, error)
}

// ErrRefused is returned when the model declines the request.
var ErrRefused = errors.New("llm: the model declined to answer")

// Provider IDs, as written in the config.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	ProviderGemini    = "gemini"
	ProviderOpenCode  = "opencode"
	ProviderOllama    = "ollama"
	ProviderCustom    = "custom"
)

// Lister is a client that can list the models its endpoint offers.
type Lister interface {
	Models(ctx context.Context) ([]string, error)
}

// MaxOutputTokens is the room every model gets for its answer, so a JSON
// object is never cut off. Summaries are capped far below it.
const MaxOutputTokens = 8192

// DefaultAnthropicModel is Claude's small, cheap model.
const DefaultAnthropicModel = "claude-haiku-5-5"

// Provider describes one summarizer backend. Every one but Anthropic speaks
// the OpenAI chat completions protocol; they differ in endpoint, default
// model and where their key comes from. Endpoint and model are only defaults:
// both can be changed, since providers rename models often.
type Provider struct {
	ID      string
	Title   string // for menus: "Anthropic API (Claude)"
	Detail  string // one line: what it is
	BaseURL string // default endpoint ("" for Anthropic, which uses its SDK)
	Model   string // default model
	EnvKey  string // environment variable the key may come from
	// KeyOptional: a local server usually needs no key.
	KeyOptional bool
	// MaxInputChars, when set, is the default conversation size for this
	// provider (sized to fit Ollama's window with the prompt and the answer).
	MaxInputChars int
}

// Providers lists every model backend, in menu order.
var Providers = []Provider{
	{ID: ProviderAnthropic, Title: "Anthropic API (Claude)", Detail: "Claude Haiku · small and cheap",
		Model: DefaultAnthropicModel, EnvKey: "ANTHROPIC_API_KEY"},
	{ID: ProviderOpenAI, Title: "OpenAI API (ChatGPT)", Detail: "GPT nano · OpenAI's cheapest model",
		BaseURL: "https://api.openai.com/v1", Model: "gpt-5.4-nano", EnvKey: "OPENAI_API_KEY"},
	{ID: ProviderGemini, Title: "Google Gemini API", Detail: "Gemini Flash-Lite · Google AI Studio key",
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", Model: "gemini-3.5-flash-lite", EnvKey: "GEMINI_API_KEY"},
	{ID: ProviderOpenCode, Title: "OpenCode Zen API", Detail: "OpenCode's gateway · free and paid models",
		BaseURL: "https://opencode.ai/zen/v1", Model: "glm-5.3-flash", EnvKey: "OPENCODE_API_KEY"},
	{ID: ProviderOllama, Title: "Ollama (this machine)", Detail: "local models · nothing leaves this machine",
		BaseURL: "http://localhost:11434", Model: "llama3.2", KeyOptional: true, MaxInputChars: 16000},
	{ID: ProviderCustom, Title: "Custom (OpenAI-compatible)", Detail: "LM Studio, OpenRouter, any endpoint",
		BaseURL: "http://localhost:1234/v1", EnvKey: "GOLDENGATE_SUMMARIZER_KEY", KeyOptional: true},
}

// Lookup finds a provider by ID.
func Lookup(id string) (Provider, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// New builds the client for a provider. Empty baseURL and model mean the
// provider's defaults; an empty key lets Anthropic's SDK find credentials
// itself and leaves the Authorization header off for the others.
func New(id, baseURL, key, model string) (Client, error) {
	p, ok := Lookup(id)
	if !ok {
		return nil, errors.New("llm: unknown provider " + id)
	}
	if model == "" {
		model = p.Model
	}
	if id == ProviderAnthropic {
		return NewAnthropic(key, model), nil
	}
	if baseURL == "" {
		baseURL = p.BaseURL
	}
	if id == ProviderOllama {
		return NewOllama(baseURL, model), nil // its native API: context size, no thinking
	}
	return NewOpenAI(id, baseURL, key, model), nil
}
