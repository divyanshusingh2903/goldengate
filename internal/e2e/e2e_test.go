// Package e2e exercises the whole product the way a new user meets it: a
// deployed bridge, `goldengate init`, share, sync, an assistant reading over
// MCP, scope separation, un-sharing, rotation and pairing (DESIGN.md 7.1, 14).
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OWNER/goldengate/internal/adapters"
	"github.com/OWNER/goldengate/internal/adapters/claudecode"
	"github.com/OWNER/goldengate/internal/cli"
	"github.com/OWNER/goldengate/internal/secrets"
	"github.com/OWNER/goldengate/internal/server"
	"github.com/OWNER/goldengate/internal/server/auth"
	"github.com/OWNER/goldengate/internal/server/store"
)

const (
	adminSecret = "Zx9Qm2Lp7Vb4Nc8Rt1Yw5Ke3Hd6Jf0GaBq+Ux/OwLk2Mn7P="
	fixture     = "../../testdata/claude-code/2.1/workitems.jsonl"
)

var fakeNow = time.Date(2026, 1, 6, 12, 0, 0, 0, time.UTC)

// bridge is a real server over a real SQLite store.
type bridge struct {
	ts     *httptest.Server
	st     store.Store
	au     *auth.Authenticator
	dbPath string
}

func startBridge(t *testing.T) *bridge { return startBridgeOpts(t, false) }

func startBridgeOpts(t *testing.T, withOAuth bool) *bridge {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "bridge.db")
	st, err := store.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	au, err := auth.New(st, auth.Options{AdminSecret: adminSecret, Now: func() time.Time { return fakeNow }})
	if err != nil {
		t.Fatal(err)
	}
	var handler http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	cfg := server.Config{Store: st, Auth: au, Now: func() time.Time { return fakeNow }, BehindProxy: true}
	if withOAuth {
		cfg.PublicURL = ts.URL
	}
	s, err := server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	handler = s
	return &bridge{ts: ts, st: st, au: au, dbPath: dbPath}
}

// uniqueClients makes every request come from a fresh address, so the
// per-address lockout (tested elsewhere) does not couple unrelated steps.
type uniqueClients struct{ n atomic.Int64 }

func (u *uniqueClients) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if r.Header.Get("X-Forwarded-For") == "" {
		n := u.n.Add(1)
		r.Header.Set("X-Forwarded-For", fmt.Sprintf("10.20.%d.%d", n/250, n%250+1))
	}
	return http.DefaultTransport.RoundTrip(r)
}

// laptop is one user's machine: config, state, secret store, session history.
type laptop struct {
	t       *testing.T
	root    string
	claude  string
	secrets *secrets.Memory
	out     *bytes.Buffer
	errOut  *bytes.Buffer
	home    string
	http    *http.Client
}

func newLaptop(t *testing.T) *laptop {
	t.Helper()
	l := &laptop{t: t, root: t.TempDir(), secrets: &secrets.Memory{}, home: "/home/user",
		http: &http.Client{Transport: &uniqueClients{}, Timeout: 20 * time.Second}}
	l.claude = filepath.Join(l.root, "claude")
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	put := func(id, body string) {
		dir := filepath.Join(l.claude, "projects", "-"+id)
		_ = os.MkdirAll(dir, 0o755)
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put("11111111-1111-4111-8111-111111111111", string(data))
	secret := strings.ReplaceAll(string(data), "/home/user/projects/demo", "/home/user/projects/client-secret")
	secret = strings.ReplaceAll(secret, "please ship the widget", "TOPSECRET-PROMPT "+"AKIA"+"IOSFODNN7EXAMPLE")
	put("22222222-2222-4222-8222-222222222222", secret)
	return l
}

func (l *laptop) deps(env map[string]string) cli.Deps {
	e := map[string]string{"XDG_CONFIG_HOME": filepath.Join(l.root, "cfg"), "XDG_STATE_HOME": filepath.Join(l.root, "state")}
	for k, v := range env {
		e[k] = v
	}
	return cli.Deps{
		Stdout: l.out, Stderr: l.errOut, Home: l.home, Getenv: func(k string) string { return e[k] },
		NewRegistry: func(string, func(string) string) (*adapters.Registry, error) {
			var r adapters.Registry
			return &r, r.Register(claudecode.New(claudecode.Options{ConfigDir: l.claude}))
		},
		Now: func() time.Time { return fakeNow }, HTTP: l.http, Hostname: func() string { return "testbox" },
		SecretStore: func(string, func(string)) secrets.Store { return l.secrets },
	}
}

func (l *laptop) run(env map[string]string, args ...string) error {
	l.out, l.errOut = &bytes.Buffer{}, &bytes.Buffer{}
	cmd := cli.New(l.deps(env))
	cmd.SetArgs(args)
	return cmd.ExecuteContext(context.Background())
}

func (l *laptop) must(env map[string]string, args ...string) string {
	l.t.Helper()
	if err := l.run(env, args...); err != nil {
		l.t.Fatalf("goldengate %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, l.out, l.errOut)
	}
	return l.out.String()
}

func (l *laptop) initEnv(b *bridge) map[string]string {
	return map[string]string{"GOLDENGATE_SERVER_URL": b.ts.URL, "GOLDENGATE_ADMIN_TOKEN": adminSecret}
}

var readTokenRe = regexp.MustCompile(`ggb_read_[A-Za-z0-9_-]{43}`)

func do(t *testing.T, method, url, token string, body string) int {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), method, url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Transport: &uniqueClients{}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func mcpSession(t *testing.T, b *bridge, token string) *mcp.ClientSession {
	t.Helper()
	hc := &http.Client{Transport: rt(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+token)
		return (&uniqueClients{}).RoundTrip(r)
	})}
	c := mcp.NewClient(&mcp.Implementation{Name: "assistant", Version: "0"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: b.ts.URL + "/mcp", HTTPClient: hc, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("assistant could not connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

type rt func(*http.Request) (*http.Response, error)

func (f rt) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func tool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || r.IsError {
		t.Fatalf("%s: %v %+v", name, err, r)
	}
	var sb strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// everythingTheServerHolds serializes all stored data and the audit log.
func everythingTheServerHolds(t *testing.T, b *bridge) string {
	t.Helper()
	ctx := context.Background()
	recs, _ := b.st.Sessions(ctx, store.SessionQuery{})
	items, _ := b.st.WorkItems(ctx, store.WorkItemQuery{})
	audit, _ := b.st.ListAudit(ctx, 1000)
	var sb strings.Builder
	for _, v := range []any{recs, items, audit} {
		j, _ := json.Marshal(v)
		sb.Write(j)
	}
	return sb.String()
}

func TestFirstRunToAssistantAnswer(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)

	// --- init: admin secret in, scoped tokens out ---
	out := l.must(l.initEnv(b), "init", "--non-interactive")
	readToken := readTokenRe.FindString(out)
	if readToken == "" {
		t.Fatalf("init must show the read token once:\n%s", out)
	}
	if strings.Count(out, readToken) != 1 {
		t.Error("the read token must be printed exactly once")
	}
	ingest, err := l.secrets.Get("ingest-token@" + strings.TrimPrefix(b.ts.URL, "http://"))
	if err != nil || !strings.HasPrefix(ingest, "ggb_ingest_") {
		t.Fatalf("ingest token must be in the secret store: %q %v", ingest, err)
	}
	if strings.Contains(out+l.errOut.String(), adminSecret) {
		t.Error("admin secret was printed")
	}
	// the admin secret is nowhere on disk, in the secret store or in the server's database
	filepath.Walk(l.root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && !strings.HasPrefix(p, l.claude) {
			if data, _ := os.ReadFile(p); bytes.Contains(data, []byte(adminSecret)) {
				t.Errorf("admin secret found on disk in %s", p)
			}
		}
		return nil
	})
	for k, v := range l.secrets.M {
		if strings.Contains(v, adminSecret) {
			t.Errorf("admin secret stored under %s", k)
		}
	}
	for _, f := range []string{b.dbPath, b.dbPath + "-wal"} { // the server's own files hold neither the admin secret nor any token in the clear
		data, _ := os.ReadFile(f)
		for name, secret := range map[string]string{"admin secret": adminSecret, "read token": readToken, "ingest token": ingest} {
			if bytes.Contains(data, []byte(secret)) {
				t.Errorf("%s found in the server's database file %s", name, filepath.Base(f))
			}
		}
	}
	toks, _ := b.st.ListTokens(context.Background())
	for _, tk := range toks {
		if strings.Contains(fmt.Sprintf("%+v", tk), readToken) || strings.Contains(fmt.Sprintf("%+v", tk), ingest) {
			t.Errorf("token secret stored server-side: %+v", tk)
		}
	}

	// from here on the admin token is not in the environment at all
	noAdmin := map[string]string{}

	// --- nothing is shared yet ---
	l.must(noAdmin, "sync")
	if ps, _ := b.st.ListProjects(context.Background()); len(ps) != 0 {
		t.Fatalf("default-deny violated, server holds %+v", ps)
	}

	// --- share one project, preview, sync ---
	l.must(noAdmin, "share", "add", "~/projects/demo")
	preview := l.must(noAdmin, "sync", "--dry-run")
	if !strings.Contains(preview, "~/projects/demo") || strings.Contains(preview, "client-secret") {
		t.Fatalf("preview:\n%s", preview)
	}
	if ps, _ := b.st.ListProjects(context.Background()); len(ps) != 0 {
		t.Fatal("dry run must send nothing")
	}
	l.must(noAdmin, "sync")
	ps, _ := b.st.ListProjects(context.Background())
	if len(ps) != 1 || ps[0].Project != "~/projects/demo" {
		t.Fatalf("server holds %+v", ps)
	}

	// --- the unselected project and the planted secret never reached the server ---
	held := everythingTheServerHolds(t, b)
	for _, leak := range []string{"client-secret", "TOPSECRET", "IOSFODNN7", "SECRET-DESCRIPTION", "SECRET-COMMENT", "/home/user"} {
		if strings.Contains(held, leak) {
			t.Errorf("%q reached the server", leak)
		}
	}

	// --- second sync sends nothing ---
	ingestsBefore := countAction(t, b, "ingest")
	l.must(noAdmin, "sync")
	if got := countAction(t, b, "ingest"); got != ingestsBefore {
		t.Errorf("second sync sent data (%d -> %d ingest calls)", ingestsBefore, got)
	}

	// --- the assistant answers "what did I work on this week?" from only that project ---
	cs := mcpSession(t, b, readToken)
	txt := tool(t, cs, "recent_activity", map[string]any{"days": 7})
	if !strings.Contains(txt, "~/projects/demo") || strings.Contains(txt, "client-secret") || !strings.Contains(txt, "untrusted") {
		t.Fatalf("recent_activity:\n%s", txt)
	}
	if prs := tool(t, cs, "list_pull_requests", nil); !strings.Contains(prs, "example/demo#7 [merged]") || !strings.Contains(prs, "NOT confirmed") {
		t.Errorf("PR evidence:\n%s", prs)
	}
	if cm := tool(t, cs, "list_commits", nil); !strings.Contains(cm, "abc1234") || !strings.Contains(cm, "[created]") {
		t.Errorf("commits:\n%s", cm)
	}

	// --- scope separation, end to end ---
	url := b.ts.URL
	proj := `{"sessions":[]}`
	for name, c := range map[string]struct {
		method, path, token, body string
		want                      int
	}{
		"read cannot ingest":         {"POST", "/v1/ingest", readToken, proj, 403},
		"read cannot delete":         {"DELETE", "/v1/projects/x", readToken, "", 403},
		"read cannot mint":           {"POST", "/v1/admin/tokens", readToken, `{"scope":"read"}`, 403},
		"ingest cannot use mcp":      {"POST", "/mcp", ingest, `{}`, 403},
		"ingest cannot mint":         {"POST", "/v1/admin/tokens", ingest, `{"scope":"read"}`, 403},
		"admin cannot read data":     {"POST", "/mcp", adminSecret, `{}`, 403},
		"admin cannot write data":    {"POST", "/v1/ingest", adminSecret, proj, 403},
		"admin cannot delete data":   {"DELETE", "/v1/projects/x", adminSecret, "", 403},
		"admin cannot list projects": {"GET", "/v1/projects", adminSecret, "", 403},
	} {
		if got := do(t, c.method, url+c.path, c.token, c.body); got != c.want {
			t.Errorf("%s: got %d want %d", name, got, c.want)
		}
	}

	// --- doctor sees a healthy server ---
	if d := l.must(noAdmin, "doctor"); !strings.Contains(d, "server  ok") {
		t.Errorf("doctor:\n%s", d)
	}

	// --- un-share: local tombstone + remote delete ---
	l.must(noAdmin, "share", "remove", "~/projects/demo", "--purge-remote")
	if !strings.Contains(l.out.String(), "deleted ~/projects/demo from the server") {
		t.Errorf("remove output:\n%s", l.out)
	}
	if ps, _ := b.st.ListProjects(context.Background()); len(ps) != 0 {
		t.Fatalf("remote data survived un-sharing: %+v", ps)
	}
	if got := tool(t, cs, "list_projects", nil); strings.Contains(got, "projects/demo") {
		t.Errorf("assistant still sees the project:\n%s", got)
	}
	if held := everythingTheServerHolds(t, b); strings.Contains(held, "Fix") || strings.Contains(held, "example/demo#7") {
		t.Errorf("work items survived un-sharing")
	}
	l.must(noAdmin, "sync")
	if ps, _ := b.st.ListProjects(context.Background()); len(ps) != 0 {
		t.Fatal("an un-shared project was pushed again")
	}

	// --- re-share, then purge on demand ---
	l.must(noAdmin, "share", "add", "~/projects/demo")
	l.must(noAdmin, "sync")
	if ps, _ := b.st.ListProjects(context.Background()); len(ps) != 1 {
		t.Fatalf("re-share must push again: %+v", ps)
	}
	l.must(noAdmin, "purge", "--project", "~/projects/demo")
	if !strings.Contains(l.out.String(), "still shared") {
		t.Errorf("purge of a still-shared project must say so:\n%s", l.out)
	}
	if ps, _ := b.st.ListProjects(context.Background()); len(ps) != 0 {
		t.Fatal("purge left data behind")
	}

	// --- rotation: the old read token dies immediately, the new one works ---
	l.must(l.initEnv(b), "token", "rotate", "read")
	newRead := readTokenRe.FindString(l.out.String())
	if newRead == "" || newRead == readToken {
		t.Fatalf("rotate must print a new read token:\n%s", l.out)
	}
	if got := do(t, "POST", url+"/mcp", readToken, `{}`); got != 401 {
		t.Errorf("rotated-out read token still accepted: %d", got)
	}
	if got := do(t, "POST", url+"/mcp", newRead, `{}`); got == 401 || got == 403 {
		t.Errorf("new read token refused: %d", got)
	}
	l.must(l.initEnv(b), "token", "rotate", "ingest")
	if got := do(t, "GET", url+"/v1/projects", ingest, ""); got != 401 {
		t.Errorf("rotated-out ingest token still accepted: %d", got)
	}
	l.must(noAdmin, "sync") // uses the replacement from the secret store
}

func countAction(t *testing.T, b *bridge, action string) int {
	t.Helper()
	evs, _ := b.st.ListAudit(context.Background(), 10000)
	n := 0
	for _, e := range evs {
		if e.Action == action {
			n++
		}
	}
	return n
}

func TestPairingSetup(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	code, _, err := b.au.CreatePairing(context.Background(), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// no admin token anywhere
	out := l.must(map[string]string{"GOLDENGATE_SERVER_URL": b.ts.URL}, "init", "--pair-code", code)
	if readTokenRe.MatchString(out) {
		t.Error("pairing yields an ingest token only; the read token comes from the server operator")
	}
	if !strings.Contains(out, "goldengatebridge token create --scope read") {
		t.Errorf("pairing output must say how to get the read token:\n%s", out)
	}
	ing, err := l.secrets.Get("ingest-token@" + strings.TrimPrefix(b.ts.URL, "http://"))
	if err != nil || !strings.HasPrefix(ing, "ggb_ingest_") {
		t.Fatalf("%q %v", ing, err)
	}
	l.must(nil, "share", "add", "~/projects/demo")
	l.must(nil, "sync")
	if ps, _ := b.st.ListProjects(context.Background()); len(ps) != 1 {
		t.Fatalf("paired laptop could not sync: %+v", ps)
	}
	// single use
	l2 := newLaptop(t)
	if err := l2.run(map[string]string{"GOLDENGATE_SERVER_URL": b.ts.URL}, "init", "--pair-code", code); err == nil {
		t.Error("a pairing code must be single use")
	}
	if len(l2.secrets.M) != 0 {
		t.Error("a failed pairing must store nothing")
	}
}

func TestInitRefusals(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	cases := map[string]struct {
		env  map[string]string
		args []string
		want string
	}{
		"plain http to a public host": {map[string]string{"GOLDENGATE_ADMIN_TOKEN": adminSecret}, []string{"init", "--non-interactive", "--server", "http://example.com"}, "https"},
		"no server":                   {map[string]string{"GOLDENGATE_ADMIN_TOKEN": adminSecret}, []string{"init", "--non-interactive"}, "server URL required"},
		"no admin token":              {map[string]string{"GOLDENGATE_SERVER_URL": b.ts.URL}, []string{"init", "--non-interactive"}, "admin token required"},
		"wrong admin token":           {map[string]string{"GOLDENGATE_SERVER_URL": b.ts.URL, "GOLDENGATE_ADMIN_TOKEN": strings.Repeat("W", 44)}, []string{"init", "--non-interactive"}, "ingest token"},
		"unreachable server":          {map[string]string{"GOLDENGATE_SERVER_URL": "http://127.0.0.1:1", "GOLDENGATE_ADMIN_TOKEN": adminSecret}, []string{"init", "--non-interactive"}, "not reachable"},
	}
	for name, c := range cases {
		err := l.run(c.env, c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want error containing %q, got %v", name, c.want, err)
		}
		if strings.Contains(fmt.Sprint(err), adminSecret) {
			t.Errorf("%s: error leaks the admin secret", name)
		}
	}
	if len(l.secrets.M) != 0 {
		t.Errorf("failed inits must store nothing: %v", l.secrets.M)
	}
	if _, err := os.Stat(filepath.Join(l.root, "cfg", "goldengate", "config.toml")); err == nil {
		t.Error("failed inits must not write a config")
	}
}

func TestAdminTokenFromStdin(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	cmdEnv := map[string]string{"GOLDENGATE_SERVER_URL": b.ts.URL}
	l.out, l.errOut = &bytes.Buffer{}, &bytes.Buffer{}
	e := map[string]string{"XDG_CONFIG_HOME": filepath.Join(l.root, "cfg"), "XDG_STATE_HOME": filepath.Join(l.root, "state")}
	for k, v := range cmdEnv {
		e[k] = v
	}
	cmd := cli.New(cli.Deps{Stdout: l.out, Stderr: l.errOut, Home: l.home, Getenv: func(k string) string { return e[k] },
		Stdin: strings.NewReader(adminSecret + "\n"), HTTP: l.http, Now: func() time.Time { return fakeNow },
		SecretStore: func(string, func(string)) secrets.Store { return l.secrets }, Hostname: func() string { return "box" }})
	cmd.SetArgs([]string{"init", "--non-interactive", "--admin-token-stdin"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, l.errOut)
	}
	if !readTokenRe.MatchString(l.out.String()) {
		t.Errorf("no read token:\n%s", l.out)
	}
}

func TestInitExplainsOAuthOnlyWhenTheServerHasIt(t *testing.T) {
	plain := newLaptop(t).must(newLaptop(t).initEnv(startBridgeOpts(t, false)), "init", "--non-interactive")
	if strings.Contains(plain, "This server has OAuth enabled") || !strings.Contains(plain, "--oauth") {
		t.Errorf("without OAuth the output should say how to enable it:\n%s", plain)
	}
	b := startBridgeOpts(t, true)
	l := newLaptop(t)
	out := l.must(l.initEnv(b), "init", "--non-interactive")
	if !strings.Contains(out, "This server has OAuth enabled") || !strings.Contains(out, b.ts.URL+"/mcp") ||
		!strings.Contains(out, "Connected Apps") || !strings.Contains(out, "oauth clients create") {
		t.Errorf("OAuth guidance missing:\n%s", out)
	}
	if !readTokenRe.MatchString(out) {
		t.Errorf("the read token is still offered for header-capable clients:\n%s", out)
	}
}

// addClaudeSession plants another Claude Code session with a different folder and prompt.
func (l *laptop) addClaudeSession(id, cwd, prompt string) {
	l.t.Helper()
	data, err := os.ReadFile(fixture)
	if err != nil {
		l.t.Fatal(err)
	}
	body := strings.ReplaceAll(string(data), "/home/user/projects/demo", cwd)
	body = strings.ReplaceAll(body, "please ship the widget", prompt)
	dir := filepath.Join(l.claude, "projects", "-"+id)
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o600); err != nil {
		l.t.Fatal(err)
	}
}

func TestWorktreeSessionsShareTheirRepositoryProject(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	l.addClaudeSession("33333333-3333-4333-8333-333333333333", "/home/user/projects/demo/.claude/worktrees/fox", "worktree-only prompt WTMARK")
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo")
	l.must(nil, "sync")

	ps, _ := b.st.ListProjects(context.Background())
	if len(ps) != 1 || ps[0].Project != "~/projects/demo" || ps[0].Sessions != 2 {
		t.Fatalf("the worktree must be filed under its repository: %+v", ps)
	}
	if held := everythingTheServerHolds(t, b); strings.Contains(held, ".claude/worktrees") {
		t.Error("the worktree path was sent")
	}

	// stop sharing only the worktree: just its sessions disappear from the server
	l.must(nil, "share", "deny", "~/projects/demo/.claude/worktrees/fox", "--purge-remote")
	if !strings.Contains(l.out.String(), "1 session(s) of ~/projects/demo/.claude/worktrees/fox") {
		t.Errorf("per-session deletion should be reported:\n%s", l.out)
	}
	ps, _ = b.st.ListProjects(context.Background())
	if len(ps) != 1 || ps[0].Sessions != 1 {
		t.Fatalf("the repository's other session must stay: %+v", ps)
	}
	if held := everythingTheServerHolds(t, b); strings.Contains(held, "WTMARK") {
		t.Error("the worktree's content survived its deletion")
	}
	l.must(nil, "sync")
	if ps, _ = b.st.ListProjects(context.Background()); ps[0].Sessions != 1 {
		t.Errorf("a denied worktree was uploaded again: %+v", ps)
	}
	// removing the repository removes the rest
	l.must(nil, "share", "remove", "~/projects/demo", "--purge-remote")
	if ps, _ = b.st.ListProjects(context.Background()); len(ps) != 0 {
		t.Fatalf("%+v", ps)
	}
}
