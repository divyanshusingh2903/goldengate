package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestOpenAICompatibleModelsList(t *testing.T) {
	var auth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen3:8b"},{"id":"models/gemini-3.5-flash-lite"},{"id":"llama3.2:latest"}]}`))
	}))
	defer ts.Close()
	c, err := New(ProviderCustom, ts.URL+"/v1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.(Lister).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gemini-3.5-flash-lite", "llama3.2:latest", "qwen3:8b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sorted, without the models/ prefix: %v", got)
	}
	if auth != "" {
		t.Error("no key, no Authorization header")
	}
	c, _ = New(ProviderGemini, ts.URL+"/v1", "k", "")
	if _, err := c.(Lister).Models(context.Background()); err != nil || auth != "Bearer k" {
		t.Errorf("the key is sent as a bearer token: %v %q", err, auth)
	}
	c, _ = New(ProviderCustom, ts.URL+"/nope", "", "")
	if _, err := c.(Lister).Models(context.Background()); err == nil {
		t.Error("a missing endpoint is an error")
	}
}

func TestCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Providers {
		if seen[p.ID] || p.Title == "" || p.Detail == "" {
			t.Errorf("provider %+v", p)
		}
		seen[p.ID] = true
		c, err := New(p.ID, "", "", "")
		if err != nil || c.Name() != p.ID+":"+p.Model {
			t.Errorf("%s: %v %v", p.ID, err, c)
		}
	}
	if _, err := New("nope", "", "", ""); err == nil {
		t.Error("unknown providers are refused")
	}
}

func TestOllamaNativeChatTurnsThinkingOffAndWidensTheWindow(t *testing.T) {
	var bodies []map[string]any
	rejectThink := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:8b"},{"name":"gemma2:2b"}]}`))
		case "/api/chat":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			bodies = append(bodies, b)
			if _, ok := b["think"]; ok && rejectThink {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"unknown field think"}`))
				return
			}
			_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"{\"ok\":true}"},"done_reason":"stop"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	c, _ := New(ProviderOllama, ts.URL+"/v1", "", "gemma2:2b") // a /v1 base still means the same server
	out, err := c.JSON(context.Background(), "sys", "user", map[string]any{"type": "object"})
	if err != nil || string(out) != `{"ok":true}` {
		t.Fatalf("%v %s", err, out)
	}
	b := bodies[0]
	opts, _ := b["options"].(map[string]any)
	if b["think"] != false || b["stream"] != false || b["format"] == nil || opts["num_ctx"] != float64(OllamaContextTokens) || opts["num_predict"] == nil {
		t.Errorf("thinking off, a wider window, a schema and room to answer: %+v", b)
	}
	models, err := c.(Lister).Models(context.Background())
	if err != nil || !reflect.DeepEqual(models, []string{"gemma2:2b", "qwen3:8b"}) {
		t.Errorf("installed models: %v %v", models, err)
	}

	rejectThink, bodies = true, nil // an Ollama too old for the field
	c, _ = New(ProviderOllama, ts.URL, "", "gemma2:2b")
	if _, err := c.JSON(context.Background(), "s", "u", map[string]any{}); err != nil || len(bodies) != 2 {
		t.Fatalf("retries without think: %v, %d calls", err, len(bodies))
	}
	if _, err := c.JSON(context.Background(), "s", "u", map[string]any{}); err != nil || len(bodies) != 3 {
		t.Errorf("and remembers it: %d calls", len(bodies))
	}
}

func TestOpenAICompatibleTurnsReasoningOffAsFarAsAllowed(t *testing.T) {
	var efforts []any
	var limits []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		efforts = append(efforts, b["reasoning_effort"])
		for _, k := range []string{"max_tokens", "max_completion_tokens"} {
			if _, ok := b[k]; ok {
				limits = append(limits, k)
			}
		}
		if b["reasoning_effort"] == "none" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unsupported value: 'reasoning_effort' does not support 'none'"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}]}`))
	}))
	defer ts.Close()
	c, _ := New(ProviderGemini, ts.URL, "k", "")
	for i := 0; i < 2; i++ {
		if _, err := c.JSON(context.Background(), "s", "u", map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(efforts, []any{"none", "minimal", "minimal"}) {
		t.Errorf("none, then minimal, and minimal from then on: %v", efforts)
	}
	if limits[0] != "max_tokens" {
		t.Errorf("an answer limit is always sent: %v", limits)
	}
	efforts, limits = nil, nil
	c, _ = New(ProviderOpenAI, ts.URL, "k", "")
	_, _ = c.JSON(context.Background(), "s", "u", map[string]any{})
	if limits[0] != "max_completion_tokens" {
		t.Errorf("OpenAI's reasoning models take max_completion_tokens: %v", limits)
	}
}
