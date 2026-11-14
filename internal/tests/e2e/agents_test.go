package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Two assistants with different project sets, connected entirely from the
// command line: the done-when of the agents CLI.
func TestTwoAssistantsWithDifferentProjectsFromTheCLI(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	admin := l.initEnv(b)
	out := l.must(admin, "init", "--non-interactive")
	if !strings.Contains(out, "This assistant can see every project you share") {
		t.Fatalf("init says what the first assistant sees:\n%s", out)
	}
	first := readTokenRe.FindString(out)
	l.must(nil, "projects", "create", "Demo", "~/projects/demo")
	l.must(nil, "projects", "create", "Client work", "~/projects/client-secret")
	l.must(nil, "sync")

	// Without the admin secret nothing about agents can be read or changed.
	if err := l.run(nil, "agents", "list"); err == nil || !strings.Contains(err.Error(), "admin token required") {
		t.Fatalf("agents needs the admin token: %v", err)
	}
	if err := l.run(map[string]string{"GOLDENGATE_ADMIN_TOKEN": "wrong"}, "agents", "list"); err == nil || !strings.Contains(err.Error(), "refused the admin token") {
		t.Fatalf("a wrong admin token is refused: %v", err)
	}

	out = l.must(admin, "agents", "list")
	if !strings.Contains(out, "Default  (agt_default)") || !strings.Contains(out, "sees: every project you share") || !strings.Contains(out, "connected: 1 token(s)") {
		t.Fatalf("the first assistant is the Default agent:\n%s", out)
	}

	// Two more assistants, each with its own project.
	out = l.must(admin, "agents", "add", "ChatGPT")
	if !strings.Contains(out, `added agent "ChatGPT"`) || !strings.Contains(out, "it sees nothing yet") {
		t.Fatalf("add: %s", out)
	}
	l.must(admin, "agents", "add", "Claude", "--max-days", "30")
	if err := l.run(admin, "agents", "add", "chatgpt"); err == nil {
		t.Error("agent names are unique")
	}
	if out := l.must(admin, "agents", "grant", "ChatGPT", "Demo"); !strings.Contains(out, `"ChatGPT" can now see Demo`) {
		t.Fatalf("grant by project name: %s", out)
	}
	if out := l.must(admin, "agents", "grant", "claude", "~/projects/client-secret"); !strings.Contains(out, `"Claude" can now see Client work`) {
		t.Fatalf("grant by folder: %s", out)
	}
	if err := l.run(admin, "agents", "grant", "ChatGPT", "No such project"); err == nil || !strings.Contains(err.Error(), "sync it first") {
		t.Fatalf("granting a project the server does not hold: %v", err)
	}
	if err := l.run(admin, "agents", "grant", "Nobody", "Demo"); err == nil || !strings.Contains(err.Error(), "no agent named") {
		t.Fatalf("unknown agent: %v", err)
	}
	gptTok := readTokenRe.FindString(l.must(admin, "agents", "token", "ChatGPT"))
	claudeTok := readTokenRe.FindString(l.must(admin, "agents", "token", "Claude", "--label", "claude desktop"))
	if gptTok == "" || claudeTok == "" || gptTok == claudeTok {
		t.Fatal("each agent gets its own token")
	}

	sees := func(tok string) string { return tool(t, mcpSession(t, b, tok), "list_projects", nil) }
	if txt := sees(gptTok); !strings.Contains(txt, "- Demo:") || strings.Contains(txt, "Client work") {
		t.Errorf("ChatGPT sees only Demo:\n%s", txt)
	}
	if txt := sees(claudeTok); !strings.Contains(txt, "- Client work:") || strings.Contains(txt, "Demo") {
		t.Errorf("Claude sees only Client work:\n%s", txt)
	}
	if txt := sees(first); !strings.Contains(txt, "- Demo:") || !strings.Contains(txt, "- Client work:") {
		t.Errorf("the first assistant sees everything shared:\n%s", txt)
	}

	out = l.must(admin, "agents", "list")
	for _, want := range []string{"ChatGPT  (agt_", "  sees: Demo\n", "Claude  (agt_", "  sees: Client work\n", "history: the last 30 days at most"} {
		if !strings.Contains(out, want) {
			t.Errorf("agents list lacks %q:\n%s", want, out)
		}
	}

	// Changes apply at once, to the tokens already handed out.
	l.must(admin, "agents", "revoke", "ChatGPT", "Demo")
	if txt := sees(gptTok); strings.Contains(txt, "Demo") {
		t.Errorf("a revoked project disappears:\n%s", txt)
	}
	l.must(admin, "agents", "revoke", "Default", "--all")
	if txt := sees(first); strings.Contains(txt, "Demo") || strings.Contains(txt, "Client work") {
		t.Errorf("turning off 'every project' leaves only explicit grants:\n%s", txt)
	}
	l.must(admin, "agents", "grant", "ChatGPT", "--all")
	if txt := sees(gptTok); !strings.Contains(txt, "- Demo:") || !strings.Contains(txt, "- Client work:") {
		t.Errorf("--all grants everything:\n%s", txt)
	}
	l.must(admin, "agents", "rename", "ChatGPT", "ChatGPT work")
	l.must(admin, "agents", "limit", "ChatGPT work", "7")
	if out := l.must(admin, "agents", "list"); !strings.Contains(out, "ChatGPT work  (agt_") || !strings.Contains(out, "history: the last 7 days at most") {
		t.Errorf("rename and limit:\n%s", out)
	}

	// Removing an agent kills its token.
	if out := l.must(admin, "agents", "remove", "Claude"); !strings.Contains(out, "1 token(s) revoked") {
		t.Fatalf("remove: %s", out)
	}
	if _, err := connectMCP(b, claudeTok); err == nil {
		t.Error("a removed agent's token must be refused")
	}

	// Rotating the default read token leaves other agents' tokens alone.
	l.must(admin, "token", "rotate", "read")
	if txt := sees(gptTok); !strings.Contains(txt, "- Demo:") {
		t.Errorf("another agent's token survives `token rotate read`:\n%s", txt)
	}
	if _, err := connectMCP(b, first); err == nil {
		t.Error("the rotated-out token must be refused")
	}

	// The ingest token cannot touch agents.
	ingest, err := l.secrets.Get("ingest-token@" + strings.TrimPrefix(b.ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.run(map[string]string{"GOLDENGATE_ADMIN_TOKEN": ingest}, "agents", "list"); err == nil {
		t.Error("the ingest token must not manage agents")
	}
}

// connectMCP tries to open an MCP session with a token; a refused token is an error.
func connectMCP(b *bridge, token string) (*mcp.ClientSession, error) {
	hc := &http.Client{Transport: rt(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+token)
		return (&uniqueClients{}).RoundTrip(r)
	})}
	c := mcp.NewClient(&mcp.Implementation{Name: "assistant", Version: "0"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: b.ts.URL + "/mcp", HTTPClient: hc, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err == nil {
		_ = cs.Close()
	}
	return cs, err
}
