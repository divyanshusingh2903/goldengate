package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/cli"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/tui"
)

func backend(l *laptop) tui.Backend { return cli.NewTUIBackend(l.deps(nil)) }

func rowOf(t *testing.T, s tui.Snapshot, label string) tui.Row {
	t.Helper()
	for _, r := range s.Rows {
		if r.Label == label {
			return r
		}
	}
	t.Fatalf("no project %s in %+v", label, s.Rows)
	return tui.Row{}
}

func TestBackendSnapshotApplyUndo(t *testing.T) {
	l := newLaptop(t)
	be := backend(l)
	ctx := context.Background()
	s, err := be.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !s.FirstRun || s.Server != "" || len(s.Rows) != 2 {
		t.Fatalf("a fresh machine: %+v", s)
	}
	demo := rowOf(t, s, "~/projects/demo")
	if demo.State != tui.NotShared || demo.Sessions != 1 || len(demo.Sources) != 1 || demo.Sources[0].Name != "claude-code" {
		t.Fatalf("nothing is shared by default: %+v", demo)
	}

	res, err := be.Apply(ctx, tui.Action{Kind: tui.ActAllow, Keys: []string{demo.Key}})
	if err != nil || res.Remote != nil {
		t.Fatalf("%v %+v", err, res)
	}
	s, _ = be.Snapshot(ctx)
	if s.FirstRun {
		t.Error("saving the config ends the first run")
	}
	if r := rowOf(t, s, "~/projects/demo"); r.State != tui.Shared || r.Pending != 1 || !strings.Contains(r.Reason, "allowed by ~/projects/demo") {
		t.Fatalf("shared, one new session: %+v", r)
	}
	if r := rowOf(t, s, "~/projects/client-secret"); r.State != tui.NotShared || r.Pending != 0 {
		t.Fatalf("others stay private: %+v", r)
	}
	if !strings.Contains(l.readConfig(t), "~/projects/demo") {
		t.Fatalf("the rule is in the config the CLI reads:\n%s", l.readConfig(t))
	}
	// the CLI and the UI see the same thing
	l.must(nil, "share", "list")
	if !strings.Contains(l.out.String(), "~/projects/demo") {
		t.Errorf("share list:\n%s", l.out)
	}

	// deny, then undo restores exactly the previous rules
	if _, err := be.Apply(ctx, tui.Action{Kind: tui.ActDeny, Keys: []string{demo.Key}}); err != nil {
		t.Fatal(err)
	}
	s, _ = be.Snapshot(ctx)
	if r := rowOf(t, s, "~/projects/demo"); r.State != tui.Denied {
		t.Fatalf("deny wins over allow: %+v", r)
	}
	if _, err := be.Undo(ctx); err != nil {
		t.Fatal(err)
	}
	s, _ = be.Snapshot(ctx)
	if r := rowOf(t, s, "~/projects/demo"); r.State != tui.Shared {
		t.Fatalf("undo restores the allow rule: %+v", r)
	}
	if _, err := be.Undo(ctx); err != nil { // undo the allow itself
		t.Fatal(err)
	}
	if _, err := be.Undo(ctx); err == nil {
		t.Error("undoing with nothing left must say so")
	}
	s, _ = be.Snapshot(ctx)
	if r := rowOf(t, s, "~/projects/demo"); r.State != tui.NotShared {
		t.Fatalf("all undone: %+v", r)
	}
}

func (l *laptop) readConfig(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(l.root, "cfg", "goldengate", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBackendRemoveExcludesWhatABroaderRuleShares(t *testing.T) {
	l := newLaptop(t)
	l.addClaudeSession("44444444-4444-4444-8444-444444444444", "/home/user/projects/demo/sub", "sub prompt")
	be := backend(l)
	ctx := context.Background()
	if _, err := be.Apply(ctx, tui.Action{Kind: tui.ActAllow, Keys: []string{"/home/user/projects"}}); err != nil { // a folder rule
		t.Fatal(err)
	}
	s, _ := be.Snapshot(ctx)
	sub := rowOf(t, s, "~/projects/demo/sub")
	if sub.State != tui.Shared {
		t.Fatalf("a folder rule covers its children: %+v", sub)
	}
	res, err := be.Apply(ctx, tui.Action{Kind: tui.ActRemove, Keys: []string{sub.Key}})
	if err != nil || !strings.Contains(res.Message, "excluded with a deny rule") {
		t.Fatalf("a covered child cannot lose the parent's rule, so it is excluded: %v %q", err, res.Message)
	}
	s, _ = be.Snapshot(ctx)
	if r := rowOf(t, s, "~/projects/demo/sub"); r.State != tui.Denied {
		t.Fatalf("excluded: %+v", r)
	}
	if r := rowOf(t, s, "~/projects/demo"); r.State != tui.Shared {
		t.Fatalf("the parent stays shared: %+v", r)
	}
	// allowing it again lifts only the exact deny rule
	if _, err := be.Apply(ctx, tui.Action{Kind: tui.ActAllow, Keys: []string{sub.Key}}); err != nil {
		t.Fatal(err)
	}
	s, _ = be.Snapshot(ctx)
	if r := rowOf(t, s, "~/projects/demo/sub"); r.State != tui.Shared {
		t.Fatalf("allowed again: %+v", r)
	}
	// a deny from a broader glob cannot be lifted from the UI
	cfgPath := filepath.Join(l.root, "cfg", "goldengate", "config.toml")
	b, _ := os.ReadFile(cfgPath)
	_ = os.WriteFile(cfgPath, []byte(strings.Replace(string(b), "deny = []", "deny = ['~/projects/**/sub']", 1)), 0o600)
	if _, err := be.Apply(ctx, tui.Action{Kind: tui.ActAllow, Keys: []string{sub.Key}}); err == nil || !strings.Contains(err.Error(), "denied by a broader rule") {
		t.Fatalf("lifting a glob deny must be refused with an explanation: %v", err)
	}
}

func TestBackendToggleSourceKeepsTheRestShared(t *testing.T) {
	l := newLaptop(t)
	be := backend(l)
	ctx := context.Background()
	s, _ := be.Snapshot(ctx)
	demo := rowOf(t, s, "~/projects/demo")
	_, _ = be.Apply(ctx, tui.Action{Kind: tui.ActAllow, Keys: []string{demo.Key}})
	res, err := be.Apply(ctx, tui.Action{Kind: tui.ActToggleSource, Keys: []string{demo.Key}, Source: "claude-code"})
	if err != nil || !strings.Contains(res.Message, "no longer shared") {
		t.Fatalf("%v %q", err, res.Message)
	}
	s, _ = be.Snapshot(ctx)
	r := rowOf(t, s, "~/projects/demo")
	if r.Sources[0].Shared || r.State == tui.Shared {
		t.Fatalf("the source is off: %+v", r)
	}
	// a sync honors it: nothing from claude-code leaves
	l.must(nil, "sync", "--dry-run")
	if strings.Contains(l.out.String(), "projects/demo") {
		t.Errorf("an excluded source was previewed for sync:\n%s", l.out)
	}
	if _, err := be.Apply(ctx, tui.Action{Kind: tui.ActToggleSource, Keys: []string{demo.Key}, Source: "claude-code"}); err != nil {
		t.Fatal(err)
	}
	s, _ = be.Snapshot(ctx)
	if r := rowOf(t, s, "~/projects/demo"); r.State != tui.Shared {
		t.Fatalf("toggled back: %+v", r)
	}
	if _, err := be.Apply(ctx, tui.Action{Kind: tui.ActToggleSource, Keys: []string{"a", "b"}, Source: "x"}); err == nil {
		t.Error("source toggling takes exactly one project")
	}
}

func TestBackendPreviewShowsExactRedactedPayloads(t *testing.T) {
	l := newLaptop(t)
	be := backend(l)
	ctx := context.Background()
	s, _ := be.Snapshot(ctx)
	secret := rowOf(t, s, "~/projects/client-secret")
	// preview works before anything is shared (that is how you decide)
	pv, err := be.Preview(ctx, secret.Key, 5)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Total != 1 || len(pv.Items) != 1 || pv.Items[0].Status != "new" {
		t.Fatalf("%+v", pv)
	}
	it := pv.Items[0]
	for _, bad := range []string{"IOSFODNN7", "TOPSECRET-PROMPT"} {
		if strings.Contains(it.JSON, bad) && bad == "IOSFODNN7" {
			t.Errorf("the previewed JSON must be the redacted payload, found %q", bad)
		}
	}
	if !json.Valid([]byte(it.JSON)) {
		t.Fatal("the paranoid view is the exact payload JSON")
	}
	var back map[string]any
	_ = json.Unmarshal([]byte(it.JSON), &back)
	if back["project"] != "~/projects/client-secret" || back["content_hash"] != it.Payload.ContentHash {
		t.Errorf("JSON does not match the payload: %v", back["project"])
	}
	if pv.Redactions["aws-access-key"] == 0 {
		t.Errorf("redaction counts reach the UI: %v", pv.Redactions)
	}
	// nothing was written by previewing
	if _, err := os.Stat(filepath.Join(l.root, "cfg", "goldengate", "config.toml")); err == nil {
		t.Error("previewing must not create a config")
	}
}

func TestBackendSyncReportsProgressAndDryRunRecordsNothing(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	l.addClaudeSession("55555555-5555-4555-8555-555555555555", "/home/user/projects/other", "other prompt")
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo")
	l.must(nil, "share", "add", "~/projects/other")
	be := backend(l)
	ctx := context.Background()

	var mu sync.Mutex
	var events []tui.SyncEvent
	emit := func(e tui.SyncEvent) { mu.Lock(); events = append(events, e); mu.Unlock() }

	plan, err := be.Plan(ctx)
	if err != nil || plan.Sessions != 2 || len(plan.Projects) != 2 || plan.NotSelected != 1 || plan.Server != b.ts.URL {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	if err := be.Sync(ctx, true, emit); err != nil {
		t.Fatal(err)
	}
	if ps, _ := b.st.ListProjects(ctx); len(ps) != 0 {
		t.Fatal("a dry run sends nothing")
	}
	plan2, _ := be.Plan(ctx)
	if plan2.Sessions != 2 {
		t.Fatal("a dry run records nothing: the plan is unchanged")
	}

	events = nil
	if err := be.Sync(ctx, false, emit); err != nil {
		t.Fatal(err)
	}
	kinds := map[tui.SyncEventKind]int{}
	for _, e := range events {
		kinds[e.Kind]++
	}
	if events[0].Kind != tui.SyncPlanned || events[0].Sessions != 2 || kinds[tui.SyncProjectStart] != 2 || kinds[tui.SyncProjectDone] != 2 || events[len(events)-1].Kind != tui.SyncFinished {
		t.Fatalf("events: %+v", events)
	}
	ps, _ := b.st.ListProjects(ctx)
	if len(ps) != 2 {
		t.Fatalf("server holds %+v", ps)
	}
	if plan3, _ := be.Plan(ctx); plan3.Sessions != 0 || plan3.Unchanged != 2 {
		t.Fatalf("after a real sync nothing is left to send: %+v", plan3)
	}
	// the unselected project never reached the server
	if held := everythingTheServerHolds(t, b); strings.Contains(held, "client-secret") || strings.Contains(held, "TOPSECRET") {
		t.Error("an unselected project reached the server through the UI's sync")
	}
}

func TestBackendSyncFailureOfOneProjectDoesNotStopTheRest(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	l.addClaudeSession("55555555-5555-4555-8555-555555555555", "/home/user/projects/other", "other prompt")
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo")
	l.must(nil, "share", "add", "~/projects/other")
	// revoke the ingest token behind the laptop's back: every push fails with 401
	toks, _ := b.st.ListTokens(context.Background())
	for _, tk := range toks {
		if tk.Scope == "ingest" {
			_ = b.st.RevokeToken(context.Background(), tk.ID, fakeNow)
		}
	}
	var events []tui.SyncEvent
	err := backend(l).Sync(context.Background(), false, func(e tui.SyncEvent) { events = append(events, e) })
	if err != nil {
		t.Fatalf("per-project failures are events, not a fatal error: %v", err)
	}
	failed := 0
	for _, e := range events {
		if e.Kind == tui.SyncProjectFailed {
			failed++
			if !strings.Contains(e.Err, "401") && !strings.Contains(e.Err, "token") {
				t.Errorf("error should say why: %q", e.Err)
			}
		}
	}
	if failed != 2 {
		t.Fatalf("both projects fail visibly: %+v", events)
	}
	if plan, _ := backend(l).Plan(context.Background()); plan.Sessions != 2 || plan.Unchanged != 0 {
		t.Errorf("failed pushes must not be recorded as synced, so both stay planned: %+v", plan)
	}
}

func TestBackendServerScreenAndRemoteDelete(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	be := backend(l)
	ctx := context.Background()
	if info, err := be.Server(ctx); err != nil || info.Configured {
		t.Fatalf("no server yet: %+v %v", info, err)
	}
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo")
	l.must(nil, "sync")
	info, err := be.Server(ctx)
	if err != nil || !info.Configured || !info.Healthy || !info.TokenOK || info.TokenStore != "memory" || len(info.Projects) != 1 || info.Projects[0].Label != "~/projects/demo" {
		t.Fatalf("%+v %v", info, err)
	}
	if err := be.DeleteServerProject(ctx, "~/projects/demo"); err != nil {
		t.Fatal(err)
	}
	if ps, _ := b.st.ListProjects(ctx); len(ps) != 0 {
		t.Fatal("deleted on the server")
	}
	// the project is still shared, so the next sync uploads it again
	l.must(nil, "sync")
	if ps, _ := b.st.ListProjects(ctx); len(ps) != 1 {
		t.Fatalf("a still-shared project re-uploads after a manual delete: %+v", ps)
	}
	// unreachable server and refused token are reported as data, not errors
	b.ts.Close()
	info, err = be.Server(ctx)
	if err != nil || info.Healthy || info.Err == "" {
		t.Fatalf("an unreachable server is a state to show: %+v %v", info, err)
	}
}

func TestBackendUnshareOffersRemoteDelete(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo")
	l.must(nil, "sync")
	be := backend(l)
	ctx := context.Background()
	s, _ := be.Snapshot(ctx)
	demo := rowOf(t, s, "~/projects/demo")
	res, err := be.Apply(ctx, tui.Action{Kind: tui.ActRemove, Keys: []string{demo.Key}})
	if err != nil || res.Remote == nil || res.Remote.Count != 1 {
		t.Fatalf("un-sharing something on the server offers to delete it: %+v %v", res, err)
	}
	if ps, _ := b.st.ListProjects(ctx); len(ps) != 1 {
		t.Fatal("nothing is deleted until the user agrees")
	}
	if _, err := be.DeleteRemote(ctx, res.Remote.Handle); err != nil {
		t.Fatal(err)
	}
	if ps, _ := b.st.ListProjects(ctx); len(ps) != 0 {
		t.Fatal("deleted after agreeing")
	}
	// and it is never uploaded again
	l.must(nil, "sync")
	if ps, _ := b.st.ListProjects(ctx); len(ps) != 0 {
		t.Fatal("an un-shared project was pushed again")
	}
}

func TestBackendSettings(t *testing.T) {
	l := newLaptop(t)
	be := backend(l)
	ctx := context.Background()
	st, err := be.Settings(ctx)
	if err != nil || st.Level != "summary" || st.Since != "90d" || len(st.Sources) != 1 || !st.Sources[0].Enabled {
		t.Fatalf("%+v %v", st, err)
	}
	st.Level, st.Since, st.EnrichGit = "titles-only", "30d", true
	st.ExtraPatterns = []string{`ACME-\d{4}`}
	if err := be.SaveSettings(ctx, st); err != nil {
		t.Fatal(err)
	}
	cfg := l.readConfig(t)
	for _, want := range []string{"titles-only", "30d", `ACME-\d{4}`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config missing %q:\n%s", want, cfg)
		}
	}
	// invalid values are refused and leave the config untouched
	before := l.readConfig(t)
	bad := st
	bad.Since = "soon"
	if err := be.SaveSettings(ctx, bad); err == nil {
		t.Error("an invalid look-back window was saved")
	}
	bad = st
	bad.ExtraPatterns = []string{"("}
	if err := be.SaveSettings(ctx, bad); err == nil || !strings.Contains(err.Error(), "regular expression") {
		t.Errorf("an invalid regex was saved: %v", err)
	}
	bad = st
	bad.Sources = []tui.SourceStatus{{Name: "claude-code", Enabled: false}}
	if err := be.SaveSettings(ctx, bad); err == nil {
		t.Error("disabling every source was allowed")
	}
	if l.readConfig(t) != before {
		t.Error("a rejected save changed the config")
	}
	// the CLI sees the new level
	l.must(nil, "share", "add", "~/projects/demo")
	l.must(nil, "sync", "--dry-run")
	if !strings.Contains(l.out.String(), `"level": "titles-only"`) {
		t.Errorf("sync uses the level chosen in the UI:\n%s", l.out)
	}
}

func TestBackendInitMatchesTheCLI(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	be := backend(l)
	res, err := be.Init(context.Background(), tui.InitParams{ServerURL: b.ts.URL, AdminToken: adminSecret})
	if err != nil {
		t.Fatal(err)
	}
	if !readTokenRe.MatchString(res.ReadToken) || res.MCPURL != b.ts.URL+"/mcp" || res.TokenStore != "memory" {
		t.Fatalf("%+v", res)
	}
	if _, err := l.secrets.Get("ingest-token@" + strings.TrimPrefix(b.ts.URL, "http://")); err != nil {
		t.Fatalf("ingest token stored: %v", err)
	}
	filepath.Walk(l.root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && !strings.HasPrefix(p, l.claude) {
			if data, _ := os.ReadFile(p); strings.Contains(string(data), adminSecret) {
				t.Errorf("admin token written to %s", p)
			}
		}
		return nil
	})
	if _, err := be.Init(context.Background(), tui.InitParams{ServerURL: "http://example.com", AdminToken: adminSecret}); err == nil {
		t.Error("plain http to a public host must be refused by the UI path too")
	}
	if _, err := be.Init(context.Background(), tui.InitParams{ServerURL: b.ts.URL, AdminToken: "wrong"}); err == nil {
		t.Error("a wrong admin token fails")
	}
	tools, err := be.Detect(context.Background())
	if err != nil || len(tools) != 1 || !tools[0].Installed || tools[0].Sessions != 2 {
		t.Fatalf("%+v %v", tools, err)
	}
}

// drive runs the real tui.Model with a real backend: messages in, commands executed.
type drive struct {
	t *testing.T
	m *tui.Model
}

func newDrive(t *testing.T, l *laptop) *drive {
	t.Helper()
	th := tui.NewTheme(tui.PlainRenderer())
	m := tui.New(backend(l), tui.Options{Theme: &th, Now: func() time.Time { return fakeNow }, StaticCursor: true})
	d := &drive{t: t, m: m}
	d.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	d.run(m.Init())
	return d
}

func (d *drive) run(cmd tea.Cmd) {
	d.t.Helper()
	if cmd == nil {
		return
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(3 * time.Second):
		d.t.Fatal("a command did not finish")
	}
	switch v := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range v {
			d.run(c)
		}
	default:
		_, next := d.m.Update(msg)
		d.run(next)
	}
}

func (d *drive) send(msg tea.Msg) {
	d.t.Helper()
	_, cmd := d.m.Update(msg)
	d.run(cmd)
}

func (d *drive) key(names ...string) {
	d.t.Helper()
	for _, n := range names {
		switch n {
		case "enter":
			d.send(tea.KeyMsg{Type: tea.KeyEnter})
		case "tab":
			d.send(tea.KeyMsg{Type: tea.KeyTab})
		case "down":
			d.send(tea.KeyMsg{Type: tea.KeyDown})
		case "space":
			d.send(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
		default:
			d.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(n)})
		}
	}
}

func (d *drive) typeText(s string) {
	for _, r := range s {
		d.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func (d *drive) mustSee(want ...string) {
	d.t.Helper()
	v := d.m.View()
	for _, w := range want {
		if !strings.Contains(v, w) {
			d.t.Fatalf("screen does not show %q:\n%s", w, v)
		}
	}
}

// The first-run experience, end to end with real code: wizard, server, token
// handling, choosing a project, the dry-run review, the first sync, and an
// assistant reading the result.
func TestFirstRunInTheTUI(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	d := newDrive(t, l)
	d.mustSee("Welcome to Golden Gate", "● Claude Code")
	d.key("enter")
	d.typeText(b.ts.URL)
	d.key("down")
	d.typeText(adminSecret)
	d.key("enter")
	d.mustSee("Connected.", "ggb_read_", "The admin token was not saved")
	if strings.Contains(d.m.View(), adminSecret) {
		t.Fatal("the admin token must never be displayed")
	}
	readToken := readTokenRe.FindString(d.m.View())
	d.key("enter") // level
	d.mustSee("What should be sent for each session?")
	d.key("enter") // summary
	d.mustSee("Choose what to share")
	// "~/projects/demo" is the most recently updated or first listed; find it and share it
	for i := 0; i < 2; i++ {
		if strings.Contains(d.m.View(), "> [ ] ~/projects/demo ") {
			break
		}
		d.key("j")
	}
	d.mustSee("> [ ] ~/projects/demo ")
	d.key("space")
	d.mustSee("> [x] ~/projects/demo ")
	d.key("tab")
	d.mustSee("Dry run: this is exactly what the first sync would send", "1 session(s) in 1 project(s)")
	d.key("enter")
	d.mustSee("Sync · dry run")
	d.key("D") // switch to a real sync
	d.mustSee("REAL SYNC")
	d.key("enter")
	d.mustSee("[ok] ~/projects/demo", "1/1 sessions", "synced: 1 session(s)")

	ps, _ := b.st.ListProjects(context.Background())
	if len(ps) != 1 || ps[0].Project != "~/projects/demo" {
		t.Fatalf("the server holds exactly the chosen project: %+v", ps)
	}
	if held := everythingTheServerHolds(t, b); strings.Contains(held, "client-secret") || strings.Contains(held, "TOPSECRET") {
		t.Fatal("an unselected project reached the server")
	}
	// the assistant, using the token the wizard showed, sees the project
	cs := mcpSession(t, b, readToken)
	if txt := tool(t, cs, "list_projects", nil); !strings.Contains(txt, "~/projects/demo") || strings.Contains(txt, "client-secret") {
		t.Fatalf("list_projects:\n%s", txt)
	}
	d.key("enter")
	d.mustSee("[x] ~/projects/demo")
	if _, err := os.Stat(filepath.Join(l.root, "state", "goldengate", "state.db")); err != nil {
		t.Errorf("state db: %v", err)
	}
}
