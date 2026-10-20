package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// OpenAI calls an OpenAI-compatible chat completions endpoint: OpenAI itself,
// Gemini's compatibility endpoint, OpenCode Zen, or a local server.
type OpenAI struct {
	provider, baseURL, apiKey, model string
	http                             *http.Client

	mu        sync.Mutex
	reasoning int // index into reasoningSteps that the endpoint accepted (or will try next)
}

// NewOpenAI builds a client for provider (used in Name). apiKey may be empty
// for local servers.
func NewOpenAI(provider, baseURL, apiKey, model string) *OpenAI {
	return &OpenAI{provider: provider, baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, model: model,
		http: &http.Client{Timeout: 3 * time.Minute}} // local models can be slow
}

// Name implements Client.
func (o *OpenAI) Name() string { return o.provider + ":" + o.model }

// reasoningSteps turn thinking off as far as each endpoint allows: "none"
// where it is accepted, else "minimal", else the provider's default (the
// field left out). An endpoint that rejects a value moves to the next one.
var reasoningSteps = []string{"none", "minimal", ""}

// JSON implements Client with response_format json_schema.
func (o *OpenAI) JSON(ctx context.Context, system, user string, schema map[string]any) ([]byte, error) {
	if o.model == "" {
		return nil, fmt.Errorf("llm: %s: no model configured", o.provider)
	}
	o.mu.Lock()
	step := o.reasoning
	o.mu.Unlock()
	for {
		body := map[string]any{
			"model": o.model,
			"messages": []map[string]string{
				{"role": "system", "content": system},
				{"role": "user", "content": user},
			},
			"response_format": map[string]any{
				"type":        "json_schema",
				"json_schema": map[string]any{"name": "session_summary", "strict": true, "schema": schema},
			},
		}
		// room for the whole answer; OpenAI's reasoning models take the newer name
		if o.provider == ProviderOpenAI {
			body["max_completion_tokens"] = MaxOutputTokens
		} else {
			body["max_tokens"] = MaxOutputTokens
			body["temperature"] = 0
		}
		if r := reasoningSteps[step]; r != "" {
			body["reasoning_effort"] = r
		}
		raw, status, err := o.post(ctx, "/chat/completions", body)
		if err != nil {
			return nil, err
		}
		if status == http.StatusBadRequest && reasoningSteps[step] != "" && strings.Contains(strings.ToLower(string(raw)), "reasoning") {
			step++ // this endpoint does not take that reasoning setting: try the next
			o.mu.Lock()
			o.reasoning = step
			o.mu.Unlock()
			continue
		}
		if status/100 != 2 {
			return nil, fmt.Errorf("llm: %s: %s: %s", o.provider, http.StatusText(status), snippet(raw))
		}
		return o.content(raw)
	}
}

func (o *OpenAI) post(ctx context.Context, path string, body map[string]any) ([]byte, int, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, 0, fmt.Errorf("llm: %s: %w", o.provider, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	resp, err := o.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("llm: %s: %w", o.provider, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return raw, resp.StatusCode, nil
}

func (o *OpenAI) content(raw []byte) ([]byte, error) {
	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("llm: %s: unreadable answer: %w", o.provider, err)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("llm: %s: no answer", o.provider)
	}
	c := out.Choices[0]
	switch {
	case c.Message.Refusal != "":
		return nil, ErrRefused
	case c.FinishReason == "length":
		return nil, fmt.Errorf("llm: %s: the answer was cut off", o.provider)
	}
	return []byte(c.Message.Content), nil
}

// snippet is the start of an error body, for messages.
func snippet(raw []byte) string {
	msg := strings.TrimSpace(string(raw))
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return msg
}

// Models implements Lister with GET {base}/models, which OpenAI, Gemini's
// compatibility endpoint, OpenCode Zen, Ollama and LM Studio all serve.
func (o *OpenAI) Models(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("llm: %s: %w", o.provider, err)
	}
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	c := *o.http
	c.Timeout = 10 * time.Second // a listing should be quick; a dead endpoint should say so fast
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: %s: %w", o.provider, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("llm: %s: %s", o.provider, resp.Status)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("llm: %s: unreadable model list: %w", o.provider, err)
	}
	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		// Gemini lists "models/gemini-…"; the chat endpoint takes the bare id
		ids = append(ids, strings.TrimPrefix(d.ID, "models/"))
	}
	sort.Strings(ids)
	return ids, nil
}
