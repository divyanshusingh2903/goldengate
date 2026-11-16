package cursor_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/cursor"
	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/workitems"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixtures = "../../../../testdata/cursor"

const (
	sessMain    = "aaaaaaaa-0000-4000-8000-000000000001"
	sessSidebar = "bbbbbbbb-0000-4000-8000-000000000002"
	sessNoDir   = "cccccccc-0000-4000-8000-000000000003"
	sessFuture  = "99999999-0000-4000-8000-000000000007"
	sessPartial = "88888888-0000-4000-8000-000000000008"
)

// install builds a Cursor user directory whose state.vscdb is made from the
// SQL fixtures.
func install(t *testing.T, scripts ...string) (*cursor.Adapter, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "globalStorage"), 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "globalStorage", "state.vscdb")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range scripts {
		b, err := os.ReadFile(filepath.Join(fixtures, f))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(context.Background(), string(b)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return cursor.New(cursor.Options{UserDir: dir, ProjectsDir: filepath.Join(fixtures, "3.30", "projects")}), dbPath
}

func list(t *testing.T, a *cursor.Adapter, opt adapters.ListOpts) (map[string]model.SessionRef, []error) {
	t.Helper()
	refs, warns := map[string]model.SessionRef{}, []error(nil)
	for ref, err := range a.ListSessions(context.Background(), opt) {
		if err != nil {
			warns = append(warns, err)
			continue
		}
		refs[ref.ID] = ref
	}
	return refs, warns
}

type view struct {
	ID, Project, Title, Version string
	Started, Updated            string
	ToolsUsed                   map[string]int
	FilesTouched                []string
	Warnings                    []string
	Turns                       []turnView
}
type turnView struct {
	Role, Text, LastText string
	Tools                []toolView `json:",omitempty"`
}
type toolView struct {
	Name, Kind, Command, Path string
	Known, OK                 bool
}

func render(s *model.RawSession) view {
	v := view{ID: s.Ref.ID, Project: s.Ref.ProjectKey, Title: s.Title, Version: s.SourceVersion, ToolsUsed: s.ToolsUsed, FilesTouched: s.FilesTouched, Warnings: s.Warnings,
		Started: s.Ref.StartedAt.Format(time.RFC3339), Updated: s.Ref.UpdatedAt.Format(time.RFC3339)}
	for _, t := range s.Turns {
		tv := turnView{Role: t.Role, Text: t.Text, LastText: t.LastText}
		for _, c := range t.Tools {
			tv.Tools = append(tv.Tools, toolView{c.Name, string(c.Kind), c.Command, c.Path, c.Result.Known, c.Result.OK})
		}
		v.Turns = append(v.Turns, tv)
	}
	return v
}

func golden(t *testing.T, name string, got any) []byte {
	t.Helper()
	b, _ := json.MarshalIndent(got, "", "  ")
	b = append(b, '\n')
	path := filepath.Join(fixtures, name)
	if *update {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return b
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run with -update): %v", name, err)
	}
	if string(want) != string(b) {
		t.Errorf("golden mismatch for %s (run -update and review)\n%s", name, b)
	}
	return b
}

func TestDetect(t *testing.T) {
	a, _ := install(t, "3.30/schema.sql", "3.30/basic.sql")
	if d, err := a.Detect(context.Background()); err != nil || !d.Installed || a.Name() != "cursor" {
		t.Fatalf("detect: %+v %v", d, err)
	}
	missing := cursor.New(cursor.Options{UserDir: t.TempDir(), ProjectsDir: t.TempDir()})
	if d, _ := missing.Detect(context.Background()); d.Installed || len(d.Notes) == 0 {
		t.Fatalf("not installed: %+v", d)
	}
	if refs, warns := list(t, missing, adapters.ListOpts{}); len(refs) != 0 || len(warns) != 0 {
		t.Fatalf("a machine without Cursor lists nothing, quietly: %v %v", refs, warns)
	}
	// a database without the conversation tables: an older or newer layout
	old, dbPath := install(t)
	db, _ := sql.Open("sqlite", "file:"+dbPath)
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE ItemTable (key TEXT, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if d, _ := old.Detect(context.Background()); d.Installed || !strings.Contains(strings.Join(d.Notes, " "), "not supported") {
		t.Fatalf("unsupported layout: %+v", d)
	}
	if _, warns := list(t, old, adapters.ListOpts{}); len(warns) != 1 || !adapters.IsSkip(warns[0]) {
		t.Fatalf("an unsupported layout is skipped with a warning: %v", warns)
	}
}

func TestListing(t *testing.T) {
	a, dbPath := install(t, "3.30/schema.sql", "3.30/basic.sql")
	refs, warns := list(t, a, adapters.ListOpts{})
	var ids []string
	for id := range refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// not listed: the never-used conversation, the draft, the sub-agent, and
	// the one whose header has an unknown shape (that one warns)
	if strings.Join(ids, " ") != strings.Join([]string{sessPartial, sessFuture, sessMain, sessSidebar, sessNoDir}, " ") {
		t.Fatalf("listed: %v", ids)
	}
	if len(warns) != 1 || !adapters.IsSkip(warns[0]) || !strings.Contains(warns[0].Error(), "unknown shape") {
		t.Fatalf("warnings: %v", warns)
	}
	m := refs[sessMain]
	if m.Source != "cursor" || m.ProjectKey != "/home/user/projects/demo" || m.Path != dbPath || m.SizeBytes == 0 ||
		m.StartedAt.Format(time.RFC3339) != "2026-01-05T09:00:00Z" || m.UpdatedAt.Format(time.RFC3339) != "2026-01-05T09:11:00Z" {
		t.Fatalf("ref: %+v", m)
	}
	if refs[sessSidebar].ProjectKey != "/home/user/projects/demo" || refs[sessNoDir].ProjectKey != cursor.NoFolder || refs[sessPartial].ProjectKey != "/home/user/projects/other" {
		t.Fatalf("project keys: %+v", refs)
	}
	if u := refs[sessNoDir]; !u.UpdatedAt.Equal(u.StartedAt) {
		t.Fatalf("without an update time, the creation time stands: %+v", u)
	}
	// since compares against the last update
	since := time.Date(2026, 1, 5, 10, 30, 0, 0, time.UTC)
	recent, _ := list(t, a, adapters.ListOpts{Since: since})
	if _, ok := recent[sessMain]; ok || len(recent) != 2 {
		t.Fatalf("since: %v", recent)
	}
}

func TestLoadGoldenAndWorkItems(t *testing.T) {
	a, _ := install(t, "3.30/schema.sql", "3.30/basic.sql")
	refs, _ := list(t, a, adapters.ListOpts{})
	var views []view
	for _, id := range []string{sessMain, sessSidebar, sessNoDir, sessPartial} {
		s, err := a.Load(context.Background(), refs[id])
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		views = append(views, render(s))
	}
	out := string(golden(t, "3.30/basic.golden.json", views))
	for _, leak := range []string{"TOPSECRET", "DRAFT-TEXT", "SUBAGENT-TEXT", "FUTURE-TEXT", "<user_query>", "<timestamp>"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q reached a loaded session", leak)
		}
	}
	main := views[0]
	if main.Title != "Ship the widget" || main.Version != "3.30.14" || len(main.Turns) != 4 || main.Turns[0].Role != "user" || main.Turns[1].Role != "assistant" ||
		main.Turns[3].LastText != "Nothing is left for this request." || len(main.FilesTouched) != 0 {
		t.Fatalf("main: %+v", main)
	}
	// every tool call of the turn is kept in order; shell results are never
	// marked as known, because Cursor records no exit code
	tools := main.Turns[1].Tools
	if len(tools) != 6 || tools[0].Kind != "file_read" || tools[0].Path != "/home/user/projects/demo/src/widget.go" || tools[1].Kind != "other" ||
		tools[2].Kind != "shell" || tools[2].Command != `git commit -m "ship the widget"` {
		t.Fatalf("tools: %+v", tools)
	}
	for _, c := range tools {
		if c.Known || c.OK {
			t.Errorf("%s is marked as a known result: %+v", c.Name, c)
		}
	}
	// the sidebar conversation gets its prompt from the transcript file,
	// without the wrapper Cursor puts around it
	if side := views[1]; len(side.Turns) != 2 || side.Turns[0].Role != "user" || side.Turns[0].Text != "find the bug in the widget" || len(side.Warnings) != 0 {
		t.Fatalf("sidebar: %+v", side)
	}
	if p := views[3]; len(p.Turns) != 2 || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "unknown shape") {
		t.Fatalf("one unreadable message is skipped with a warning: %+v", p)
	}

	// work items: with no proof of what a command did, everything is a mention
	s, _ := a.Load(context.Background(), refs[sessMain])
	items := workitems.Extract(s, workitems.Options{})
	got := map[string]string{}
	for _, it := range items {
		got[it.Kind+" "+it.Key] = it.Status
	}
	want := map[string]string{"pull_request example/demo#7": "mentioned", "issue linear:LIN-9": "mentioned"}
	if len(got) != len(want) {
		t.Fatalf("work items: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}

func TestUnknownFormatsAreSkippedNotGuessed(t *testing.T) {
	a, _ := install(t, "3.30/schema.sql", "3.30/basic.sql")
	refs, _ := list(t, a, adapters.ListOpts{})
	if _, err := a.Load(context.Background(), refs[sessFuture]); !adapters.IsSkip(err) || !strings.Contains(err.Error(), "unknown message format") {
		t.Fatalf("a conversation in a future format is skipped: %v", err)
	}
	gone := refs[sessMain]
	gone.ID = "00000000-0000-4000-8000-000000000000"
	if _, err := a.Load(context.Background(), gone); !adapters.IsSkip(err) {
		t.Fatalf("a conversation that no longer exists: %v", err)
	}
	// without the transcript file the sidebar conversation still loads, and says its prompt is missing
	b, _ := install(t, "3.30/schema.sql", "3.30/basic.sql")
	bare := cursor.New(cursor.Options{UserDir: filepath.Dir(filepath.Dir(refsPath(t, b))), ProjectsDir: t.TempDir()})
	brefs, _ := list(t, bare, adapters.ListOpts{})
	s, err := bare.Load(context.Background(), brefs[sessSidebar])
	if err != nil || len(s.Turns) != 1 || s.Turns[0].Role != "assistant" || len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "prompt") {
		t.Fatalf("no transcript: %+v %v", s, err)
	}
}

func refsPath(t *testing.T, a *cursor.Adapter) string {
	t.Helper()
	refs, _ := list(t, a, adapters.ListOpts{})
	return refs[sessMain].Path
}

// The adapter must leave Cursor's database exactly as it found it.
func TestReadOnly(t *testing.T) {
	a, dbPath := install(t, "3.30/schema.sql", "3.30/basic.sql")
	before, _ := os.ReadFile(dbPath)
	refs, _ := list(t, a, adapters.ListOpts{})
	for _, r := range refs {
		_, _ = a.Load(context.Background(), r)
	}
	after, _ := os.ReadFile(dbPath)
	if string(before) != string(after) {
		t.Fatal("the database changed")
	}
	entries, _ := os.ReadDir(filepath.Dir(dbPath))
	if len(entries) != 1 {
		t.Fatalf("files were created next to the database: %v", entries)
	}
}
