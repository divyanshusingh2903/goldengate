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

// Ollama talks to a local Ollama through its native API, which (unlike its
// OpenAI-compatible one) can set the context window, turn thinking off and
// constrain the answer to a JSON schema.
type Ollama struct {
	root, model string
	http        *http.Client

	mu      sync.Mutex
	noThink bool // false once the server rejected the think field
}

// Ollama runtime settings for summaries.
const (
	OllamaContextTokens = 8192 // Ollama's default window is 4096, too small for a session
	ollamaAnswerTokens  = 2048
)

// NewOllama builds a client. baseURL may be the server root or its /v1
// OpenAI-compatible path; both mean the same server.
func NewOllama(baseURL, model string) *Ollama {
	root := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
	return &Ollama{root: root, model: model, noThink: true, http: &http.Client{Timeout: 5 * time.Minute}}
}

// Name implements Client.
func (o *Ollama) Name() string { return ProviderOllama + ":" + o.model }

// JSON implements Client with /api/chat.
func (o *Ollama) JSON(ctx context.Context, system, user string, schema map[string]any) ([]byte, error) {
	if o.model == "" {
		return nil, fmt.Errorf("llm: ollama: no model configured")
	}
	for {
		o.mu.Lock()
		noThink := o.noThink
		o.mu.Unlock()
		body := map[string]any{
			"model": o.model,
			"messages": []map[string]string{
				{"role": "system", "content": system},
				{"role": "user", "content": user},
			},
			"stream": false,
			"format": schema,
			"options": map[string]any{
				"num_ctx":     OllamaContextTokens,
				"num_predict": ollamaAnswerTokens,
				"temperature": 0,
			},
		}
		if noThink {
			body["think"] = false // thinking models answer at once; others ignore it
		}
		raw, status, err := o.do(ctx, http.MethodPost, "/api/chat", body)
		if err != nil {
			return nil, err
		}
		if status == http.StatusBadRequest && noThink && strings.Contains(strings.ToLower(string(raw)), "think") {
			o.mu.Lock()
			o.noThink = false // an older Ollama that does not know the field
			o.mu.Unlock()
			continue
		}
		if status/100 != 2 {
			return nil, fmt.Errorf("llm: ollama: %s: %s", http.StatusText(status), snippet(raw))
		}
		var out struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			DoneReason string `json:"done_reason"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("llm: ollama: unreadable answer: %w", err)
		}
		if out.DoneReason == "length" {
			return nil, fmt.Errorf("llm: ollama: the answer was cut off")
		}
		return []byte(out.Message.Content), nil
	}
}

// Models implements Lister with /api/tags: the models installed here.
func (o *Ollama) Models(ctx context.Context) ([]string, error) {
	c := *o.http
	c.Timeout = 10 * time.Second
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.root+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("llm: ollama: %w", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: ollama: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("llm: ollama: %s", resp.Status)
	}
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("llm: ollama: unreadable model list: %w", err)
	}
	var ids []string
	for _, m := range out.Models {
		ids = append(ids, m.Name)
	}
	sort.Strings(ids)
	return ids, nil
}

func (o *Ollama) do(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, o.root+path, bytes.NewReader(b))
	if err != nil {
		return nil, 0, fmt.Errorf("llm: ollama: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("llm: ollama: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return raw, resp.StatusCode, nil
}
