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
		answer := `{"title":"Ship the widget","abstract":"Shipped the widget.","narrative":"Built and committed the widget. A rewrite was considered and rejected.\n\nNothing is left open.","goals":["Ship the widget"],"open_problems":[]}`
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
	for _, want := range []string{`"summarized_by":"custom:tiny"`, "Built and committed the widget", `"title":"Ship the widget"`,
		`"narrative_by":"custom:tiny"`, `"narrative_length":"standard"`, `"abstract":"Shipped the widget."`, `rejected.\n\nNothing is left open.`} {
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

	// Changing the narrative length alone re-summarizes nothing: it waits
	// for --resummarize, like turning a model on.
	key := map[string]string{"GOLDENGATE_SUMMARIZER_KEY": "sk-local"}
	l.must(nil, "summarizer", "use", "custom", "--model", "tiny", "--base-url", ts.URL+"/v1", "--length", "detailed")
	l.must(key, "sync")
	if len(fake.bodies) != 0 {
		t.Fatalf("a new length does not re-summarize on its own: %d calls", len(fake.bodies))
	}
	if plan, err := backend(l).Plan(context.Background(), tui.SyncOpts{}); err != nil || plan.Stale != 1 {
		t.Fatalf("the session counts as stale under the new length: %v %+v", err, plan)
	}
	l.must(key, "sync", "--resummarize")
	if len(fake.bodies) != 1 || !strings.Contains(fake.bodies[0], "several paragraphs") {
		t.Fatalf("with the flag the model is asked once, for the detailed length: %d calls", len(fake.bodies))
	}
	if held := everythingTheServerHolds(t, b); !strings.Contains(held, `"narrative_length":"detailed"`) {
		t.Errorf("the server holds the new narrative:\n%s", held)
	}
	if err := l.run(nil, "summarizer", "use", "custom", "--length", "epic"); err == nil {
		t.Error("an unknown length is refused")
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

func TestRenamingOrMergingAProjectReachesTheServerOnTheNextSync(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	fake := &fakeChatServer{}
	ts := fake.start(t)
	ctx := context.Background()
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "projects", "create", "Widgets", "~/projects/demo")
	l.must(nil, "sync")
	ps, _ := b.st.ListProjects(ctx)
	if len(ps) != 1 || ps[0].Project != "Widgets" || ps[0].Sessions != 1 {
		t.Fatalf("first sync: %+v", ps)
	}
	id := ps[0].ID
	ingests := countAction(t, b, "ingest")

	// nothing changed: nothing is sent
	l.must(nil, "sync")
	if got := countAction(t, b, "ingest"); got != ingests {
		t.Fatalf("an unchanged project must not be re-sent: %d -> %d", ingests, got)
	}

	// a rename re-sends the stored payloads under the new name; with a model
	// on, it must not ask the model anything
	l.must(nil, "summarizer", "use", "custom", "--model", "tiny", "--base-url", ts.URL+"/v1")
	l.must(nil, "projects", "rename", "Widgets", "Widget work")
	l.must(map[string]string{"GOLDENGATE_SUMMARIZER_KEY": "sk-local"}, "sync")
	if len(fake.bodies) != 0 {
		t.Fatalf("re-filing a session never calls the model: %d calls", len(fake.bodies))
	}
	if !strings.Contains(l.errOut.String(), "1 synced sessions re-sent as they were") {
		t.Errorf("the run says why it sent something:\n%s", l.errOut)
	}
	ps, _ = b.st.ListProjects(ctx)
	if len(ps) != 1 || ps[0].Project != "Widget work" || ps[0].ID != id || ps[0].Sessions != 1 {
		t.Fatalf("the server shows the new name under the same id: %+v", ps)
	}
	if held := everythingTheServerHolds(t, b); !strings.Contains(held, `"summarized_by":"heuristic"`) {
		t.Errorf("the summary itself is untouched:\n%s", held)
	}
	l.must(nil, "summarizer", "use", "heuristic")

	// a merge moves the sessions to the other project's id; the emptied one goes
	l.must(nil, "projects", "create", "Everything")
	l.must(nil, "projects", "merge", "Widget work", "Everything")
	l.must(nil, "sync")
	ps, _ = b.st.ListProjects(ctx)
	if len(ps) != 1 || ps[0].Project != "Everything" || ps[0].ID == id || ps[0].Sessions != 1 {
		t.Fatalf("after the merge: %+v", ps)
	}
	ingests = countAction(t, b, "ingest")
	l.must(nil, "sync")
	if got := countAction(t, b, "ingest"); got != ingests {
		t.Errorf("once re-filed, nothing more is sent: %d -> %d", ingests, got)
	}
}
