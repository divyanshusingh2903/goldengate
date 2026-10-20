package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/tui"
)

// fakeChatServer is an OpenAI-compatible endpoint that records what it is sent.
type fakeChatServer struct {
	mu     sync.Mutex
	bodies []string
	auth   []string
}

func (f *fakeChatServer) start(t *testing.T) *httptest.Server {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, string(b))
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		answer := `{"title":"Ship the widget","goals":["Ship the widget"],"description":["Built and committed the widget"],"outcomes":["Committed"]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": answer}}}})
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestSummarizerCommands(t *testing.T) {
	l := newLaptop(t)
	if out := l.must(nil, "summarizer", "status"); !strings.Contains(out, "heuristic") {
		t.Fatalf("the default is the local heuristic: %s", out)
	}
	if err := l.run(nil, "summarizer", "use", "nonsense"); err == nil || !strings.Contains(err.Error(), "anthropic|openai|gemini|opencode|ollama|custom") {
		t.Fatalf("unknown providers are refused with the list: %v", err)
	}
	for id, want := range map[string]string{
		"openai":   "openai:gpt-5.4-nano, API key: none, endpoint https://api.openai.com/v1",
		"gemini":   "gemini:gemini-3.5-flash-lite, API key: none, endpoint https://generativelanguage.googleapis.com/v1beta/openai",
		"opencode": "opencode:glm-5.3-flash, API key: none, endpoint https://opencode.ai/zen/v1",
		"ollama":   "ollama:llama3.2, API key: none, endpoint http://localhost:11434",
		"custom":   "custom:, API key: none, endpoint http://localhost:1234/v1",
	} {
		l.must(nil, "summarizer", "use", id)
		if out := l.must(nil, "summarizer", "status"); !strings.Contains(out, want) {
			t.Errorf("%s defaults: %s", id, out)
		}
	}
	l.must(nil, "summarizer", "use", "gemini")
	if out := l.must(map[string]string{"GEMINI_API_KEY": "k"}, "summarizer", "status"); !strings.Contains(out, "$GEMINI_API_KEY") {
		t.Errorf("each provider reads its own key variable: %s", out)
	}
	out := l.must(nil, "summarizer", "use", "anthropic")
	for _, want := range []string{"anthropic:claude-haiku-5-5", "redacted conversation", "Tool output and file contents are never", "set-key anthropic"} {
		if !strings.Contains(out, want) {
			t.Errorf("use anthropic says %q:\n%s", want, out)
		}
	}
	if out := l.must(map[string]string{"ANTHROPIC_API_KEY": "sk-test"}, "summarizer", "status"); !strings.Contains(out, "API key: $ANTHROPIC_API_KEY") || strings.Contains(out, "sk-test") {
		t.Errorf("status names the key's source, never the key: %s", out)
	}
	if out := l.must(nil, "doctor"); !strings.Contains(out, "summary ok    anthropic:claude-haiku-5-5") {
		t.Errorf("doctor shows the summarizer:\n%s", out)
	}
	l.must(nil, "summarizer", "use", "heuristic")
	if out := l.must(nil, "summarizer", "status"); !strings.Contains(out, "heuristic") {
		t.Errorf("back to the heuristic: %s", out)
	}
}

func TestSyncWithAModelSummarizer(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	fake := &fakeChatServer{}
	ts := fake.start(t)
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo")
	l.must(nil, "summarizer", "use", "custom", "--model", "tiny", "--base-url", ts.URL+"/v1")
	if out := l.must(nil, "summarizer", "test"); !strings.Contains(out, "custom:tiny ok") {
		t.Fatalf("test reaches the endpoint: %s", out)
	}
	fake.bodies, fake.auth = nil, nil
	l.must(map[string]string{"GOLDENGATE_SUMMARIZER_KEY": "sk-local"}, "sync")

	if len(fake.bodies) != 1 {
		t.Fatalf("one shared session, one model call: %d", len(fake.bodies))
	}
	sent := fake.bodies[0]
	for _, leak := range []string{"TOPSECRET", "IOSFODNN7", "client-secret"} {
		if strings.Contains(sent, leak) {
			t.Errorf("%q reached the model", leak)
		}
	}
	if !strings.Contains(sent, "please ship the widget") || !strings.Contains(sent, `"json_schema"`) || fake.auth[0] != "Bearer sk-local" {
		t.Errorf("the request carries the shared conversation, the schema and the key:\n%s", sent)
	}
	held := everythingTheServerHolds(t, b)
	for _, want := range []string{`"summarized_by":"custom:tiny"`, "Built and committed the widget", `"title":"Ship the widget"`} {
		if !strings.Contains(held, want) {
			t.Errorf("the server holds the model's summary (%s)", want)
		}
	}
	// a second sync sends nothing and asks the model nothing
	fake.bodies = nil
	l.must(nil, "sync")
	if len(fake.bodies) != 0 {
		t.Errorf("unchanged sessions are not summarized again: %d calls", len(fake.bodies))
	}
}

func TestResummarizeUpgradesSessionsSyncedWithTheHeuristic(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	fake := &fakeChatServer{}
	ts := fake.start(t)
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo")
	l.must(nil, "sync") // with the heuristic
	if held := everythingTheServerHolds(t, b); !strings.Contains(held, `"summarized_by":"heuristic"`) {
		t.Fatalf("first synced with the heuristic:\n%s", held)
	}
	if err := l.run(nil, "sync", "--resummarize"); err == nil || !strings.Contains(err.Error(), "summarizer") {
		t.Fatalf("--resummarize needs a model: %v", err)
	}

	l.must(nil, "summarizer", "use", "custom", "--model", "tiny", "--base-url", ts.URL+"/v1")
	key := map[string]string{"GOLDENGATE_SUMMARIZER_KEY": "sk-local"}
	l.must(key, "sync")
	if len(fake.bodies) != 0 || !strings.Contains(l.errOut.String(), "1 synced sessions still have summaries not written by custom:tiny") {
		t.Fatalf("turning the model on alone sends nothing, but says what is left: %d calls\n%s", len(fake.bodies), l.errOut)
	}

	if plan, err := backend(l).Plan(context.Background(), tui.SyncOpts{}); err != nil || plan.Stale != 1 || plan.Summarizer != "custom:tiny" || plan.Sessions != 0 {
		t.Fatalf("the TUI's review counts the heuristic session: %v %+v", err, plan)
	}

	l.must(key, "sync", "--resummarize")
	if len(fake.bodies) != 1 {
		t.Fatalf("the one synced session goes to the model once: %d calls", len(fake.bodies))
	}
	for _, leak := range []string{"TOPSECRET", "IOSFODNN7", "client-secret"} {
		if strings.Contains(fake.bodies[0], leak) {
			t.Errorf("%q reached the model", leak)
		}
	}
	if !strings.Contains(l.errOut.String(), "1 synced sessions re-summarized by custom:tiny") {
		t.Errorf("the run reports the upgrade:\n%s", l.errOut)
	}
	held := everythingTheServerHolds(t, b)
	if !strings.Contains(held, `"summarized_by":"custom:tiny"`) || strings.Contains(held, `"summarized_by":"heuristic"`) {
		t.Errorf("the server now holds the model's summary:\n%s", held)
	}

	fake.bodies = nil
	l.must(key, "sync", "--resummarize")
	if len(fake.bodies) != 0 {
		t.Errorf("an upgraded session is not summarized again: %d calls", len(fake.bodies))
	}
}
