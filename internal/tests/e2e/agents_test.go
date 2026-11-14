package e2e

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/tui"
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
	l.must(nil, "projects", "create", "Demo", "~/projects/demo", "--auto-approve")
	l.must(nil, "projects", "create", "Client work", "~/projects/client-secret", "--auto-approve")
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

// The TUI's Agents screen works through the same backend: list, review-and-
// apply changes, see what an agent can read, revoke a token.
func TestBackendAgentsGridViewAsAndRevoke(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	admin := l.initEnv(b)
	l.must(admin, "init", "--non-interactive")
	l.must(nil, "projects", "create", "Demo", "~/projects/demo", "--auto-approve")
	l.must(nil, "projects", "create", "Client work", "~/projects/client-secret", "--auto-approve")
	l.must(nil, "sync")
	l.must(admin, "agents", "add", "ChatGPT")
	gptTok := readTokenRe.FindString(l.must(admin, "agents", "token", "ChatGPT", "--label", "connector"))
	be := backend(l)
	ctx := context.Background()

	if _, err := be.Agents(ctx, "wrong"); err == nil || !strings.Contains(err.Error(), "refused the admin token") {
		t.Fatalf("a wrong secret is refused: %v", err)
	}
	if _, err := be.Agents(ctx, ""); err == nil {
		t.Fatal("no secret, no listing")
	}
	info, err := be.Agents(ctx, adminSecret)
	if err != nil || len(info.Agents) != 2 || len(info.Projects) != 2 {
		t.Fatalf("agents: %+v %v", info, err)
	}
	var def, gpt tui.AgentInfo
	for _, a := range info.Agents {
		switch a.Name {
		case "Default":
			def = a
		case "ChatGPT":
			gpt = a
		}
	}
	if !def.AllProjects || gpt.AllProjects || len(gpt.Grants) != 0 || len(gpt.Tokens) != 1 || gpt.Tokens[0].Label != "connector" || !gpt.LastUsed.IsZero() {
		t.Fatalf("default sees all, ChatGPT nothing yet: %+v %+v", def, gpt)
	}
	ids := map[string]string{}
	for _, p := range info.Projects {
		ids[p.Name] = p.ID
	}

	// Nothing granted: the view is empty.
	v, err := be.AgentView(ctx, adminSecret, gpt.ID)
	if err != nil || len(v.Projects) != 0 || len(v.Sessions) != 0 || len(v.Work) != 0 || v.Note == "" {
		t.Fatalf("an agent with no grants reads nothing: %+v %v", v, err)
	}
	// Apply a reviewed change, then the view matches what the assistant gets.
	change := []tui.GrantChange{{AgentID: gpt.ID, AgentName: "ChatGPT", ProjectID: ids["Demo"], ProjectName: "Demo", Grant: true}}
	if err := be.ApplyGrants(ctx, "wrong", change); err == nil {
		t.Fatal("applying needs the admin secret")
	}
	if err := be.ApplyGrants(ctx, adminSecret, change); err != nil {
		t.Fatal(err)
	}
	v, err = be.AgentView(ctx, adminSecret, gpt.ID)
	if err != nil || len(v.Projects) != 1 || v.Projects[0].Name != "Demo" || len(v.Sessions) != 1 || v.Sessions[0].Title != "please ship the widget" || v.Sessions[0].Project != "Demo" {
		t.Fatalf("the view after a grant: %+v %v", v, err)
	}
	blob := fmt.Sprintf("%+v", v)
	if strings.Contains(blob, "TOPSECRET") || strings.Contains(blob, "Client work") {
		t.Fatalf("the view shows only granted projects: %s", blob)
	}
	if len(v.Work) == 0 {
		t.Fatalf("work items of the granted project are listed: %+v", v)
	}
	cs := mcpSession(t, b, gptTok)
	if txt := tool(t, cs, "recent_activity", nil); !strings.Contains(txt, "please ship the widget") || strings.Contains(txt, "TOPSECRET") {
		t.Fatalf("the assistant gets what the view said:\n%s", txt)
	}
	// The switch, and taking a project away again.
	if err := be.ApplyGrants(ctx, adminSecret, []tui.GrantChange{{AgentID: gpt.ID, All: true, Grant: true}, {AgentID: gpt.ID, ProjectID: ids["Demo"], Grant: false}}); err != nil {
		t.Fatal(err)
	}
	if v, _ := be.AgentView(ctx, adminSecret, gpt.ID); len(v.Projects) != 2 {
		t.Fatalf("every project: %+v", v.Projects)
	}
	info, _ = be.Agents(ctx, adminSecret)
	for _, a := range info.Agents {
		if a.ID == gpt.ID && (!a.AllProjects || len(a.Grants) != 0 || a.LastUsed.IsZero()) {
			t.Fatalf("after the changes, and once used: %+v", a)
		}
	}
	// Revoking the token cuts the assistant off.
	if err := be.RevokeAgentToken(ctx, adminSecret, gpt.Tokens[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := connectMCP(b, gptTok); err == nil {
		t.Error("a revoked token must be refused")
	}
	if _, err := be.AgentView(ctx, adminSecret, "agt_nope"); err == nil {
		t.Error("an unknown agent has no view")
	}
	// The admin secret is never written anywhere by any of this.
	for k, val := range l.secrets.M {
		if strings.Contains(val, adminSecret) {
			t.Errorf("the admin secret reached the keyring (%s)", k)
		}
	}
	if strings.Contains(l.readConfig(t), adminSecret) {
		t.Error("the admin secret reached the config")
	}
}
