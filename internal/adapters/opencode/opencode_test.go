package opencode_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/OWNER/goldengate/internal/adapters"
	"github.com/OWNER/goldengate/internal/adapters/opencode"
	"github.com/OWNER/goldengate/internal/model"
	"github.com/OWNER/goldengate/internal/workitems"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixtures = "../../../testdata/opencode"

// install builds a data dir whose opencode.db is made from the SQL fixtures.
func install(t *testing.T, scripts ...string) (*opencode.Adapter, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
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
	return opencode.New(opencode.Options{DataDir: dir}), dbPath
}

func list(t *testing.T, a *opencode.Adapter) (map[string]model.SessionRef, []error) {
	t.Helper()
	refs, warns := map[string]model.SessionRef{}, []error(nil)
	for ref, err := range a.ListSessions(context.Background(), adapters.ListOpts{}) {
		if err != nil {
			warns = append(warns, err)
			continue
		}
		refs[ref.ID] = ref
	}
	return refs, warns
}

func load(t *testing.T, a *opencode.Adapter, ref model.SessionRef) *model.RawSession {
	t.Helper()
	s, err := a.Load(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type view struct {
	ID, Project, Title, Version string
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
	Name, Kind, Command, Path, Input string
	Created, Known, OK               bool
	Exit                             *int
}

func render(s *model.RawSession) view {
	v := view{ID: s.Ref.ID, Project: s.Ref.ProjectKey, Title: s.Title, Version: s.SourceVersion, ToolsUsed: s.ToolsUsed, FilesTouched: s.FilesTouched, Warnings: s.Warnings}
	for _, t := range s.Turns {
		tv := turnView{Role: t.Role, Text: t.Text, LastText: t.LastText}
		for _, c := range t.Tools {
			tv.Tools = append(tv.Tools, toolView{c.Name, string(c.Kind), c.Command, c.Path, string(c.Input), c.Created, c.Result.Known, c.Result.OK, c.Result.ExitCode})
		}
		v.Turns = append(v.Turns, tv)
	}
	return v
}

func golden(t *testing.T, name string, got any) {
	t.Helper()
	b, _ := json.MarshalIndent(got, "", "  ")
	b = append(b, '\n')
	path := filepath.Join(fixtures, name)
	if *update {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run with -update): %v", name, err)
	}
	if string(want) != string(b) {
		t.Errorf("golden mismatch for %s (run -update and review)\n%s", name, b)
	}
}

func TestListing(t *testing.T) {
	a, _ := install(t, "1.18/schema.sql", "1.18/basic.sql")
	refs, warns := list(t, a)
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
	if _, ok := refs["ses_child"]; ok {
		t.Error("sub-agent sessions must not be listed on their own")
	}
	if len(refs) != 4 {
		t.Fatalf("want 4 top-level sessions, got %d: %v", len(refs), refs)
	}
	main := refs["ses_main"]
	if main.Source != "opencode" || main.ProjectKey != "/home/user/projects/demo" || main.RepoRoot != "/home/user/projects/demo" || main.SizeBytes != 3 {
		t.Errorf("main: %+v", main)
	}
	if !main.UpdatedAt.After(main.StartedAt) || main.StartedAt.Year() != 2026 {
		t.Errorf("times: %v %v", main.StartedAt, main.UpdatedAt)
	}
	wt := refs["ses_wt"]
	if wt.ProjectKey != "/home/user/projects/demo-wt" || wt.RepoRoot != "/home/user/projects/demo" {
		t.Errorf("worktree sessions must expose their repository root: %+v", wt)
	}
	if refs["ses_empty"].RepoRoot != "" {
		t.Errorf("the global project (worktree \"/\") is not a repo root: %q", refs["ses_empty"].RepoRoot)
	}
	// since filter
	n := 0
	for range a.ListSessions(context.Background(), adapters.ListOpts{Since: time.Now().Add(time.Hour)}) {
		n++
	}
	if n != 0 {
		t.Errorf("since: %d", n)
	}
}

func TestLoadGoldenAndHygiene(t *testing.T) {
	a, _ := install(t, "1.18/schema.sql", "1.18/basic.sql")
	refs, _ := list(t, a)
	s := load(t, a, refs["ses_main"])
	golden(t, "1.18/basic.golden.json", render(s))
	blob, _ := json.Marshal(render(s))
	for _, bad := range []string{"INJECTED CONTEXT", "private thoughts", "file contents MUST NOT", "TOPSECRET", "SECRET-ISSUE-BODY", "SECRET-COMMENT"} {
		if strings.Contains(string(blob), bad) {
			t.Errorf("%q leaked into the normalized session", bad)
		}
	}
	if s.Title != "Fix the widget" || s.SourceVersion != "1.18.30" || len(s.Warnings) != 0 {
		t.Errorf("title/version/warnings: %q %q %v", s.Title, s.SourceVersion, s.Warnings)
	}
	if len(s.Turns) != 2 || s.Turns[1].LastText != "Done. I also looked at PR #12 and commit deadbee1." {
		t.Errorf("assistant messages must merge into one turn: %d", len(s.Turns))
	}
	if s2 := load(t, a, refs["ses_empty"]); s2.Title != "" {
		t.Errorf("placeholder titles must be dropped: %q", s2.Title)
	}
	if s3 := load(t, a, refs["ses_old"]); !strings.Contains(strings.Join(s3.Warnings, "|"), "untested OpenCode version 0.9.0") {
		t.Errorf("version warning: %v", s3.Warnings)
	}
}

func TestEvidenceSemantics(t *testing.T) {
	a, _ := install(t, "1.18/schema.sql", "1.18/basic.sql")
	refs, _ := list(t, a)
	items := workitems.Extract(load(t, a, refs["ses_main"]), workitems.Options{})
	status := map[string]string{}
	for _, it := range items {
		status[it.Kind+" "+it.Key] = it.Status
	}
	want := map[string]string{
		"commit example/demo@abc1234":  "created",   // exit 0
		"commit example/demo@deadbee1": "mentioned", // text only
		"pull_request example/demo#7":  "merged",
		"pull_request example/demo#12": "mentioned",
		"file src/new.go":              "created", // write on a path that did not exist
		"file src/old.go":              "updated", // write over an existing file
		"file src/a.go":                "updated",
		"file src/p.go":                "created",   // apply_patch Add
		"file src/q.go":                "updated",   // apply_patch Update
		"issue github:example/demo#3":  "completed", // created, then closed as completed
		"issue github:example/demo#4":  "mentioned", // only read
	}
	for k, v := range want {
		if status[k] != v {
			t.Errorf("%s: got %q want %q", k, status[k], v)
		}
	}
	for _, absent := range []string{"commit example/demo@7777777", "commit example/demo@1111111", "file src/failed.go", "file .env"} {
		if _, ok := status[absent]; ok {
			t.Errorf("%s must not be a work item", absent)
		}
	}
	if len(items) != len(want) {
		t.Errorf("unexpected extra items: %v", status)
	}
	all, _ := json.Marshal(items)
	if strings.Contains(string(all), "SECRET-") {
		t.Error("issue bodies or comments leaked into work items")
	}
}

func TestNeverTouchesOtherTables(t *testing.T) {
	// 1. the source only names the four allowed tables
	src, err := os.ReadFile("opencode.go")
	if err != nil {
		t.Fatal(err)
	}
	literal := regexp.MustCompile("`[^`]*`") // SQL lives only in raw string literals
	from := regexp.MustCompile(`(?i)\b(?:FROM|JOIN)\s+(\w+)`)
	allowed := map[string]bool{"session": true, "message": true, "part": true, "project": true}
	seen := 0
	for _, lit := range literal.FindAllString(string(src), -1) {
		for _, m := range from.FindAllStringSubmatch(lit, -1) {
			seen++
			if !allowed[strings.ToLower(m[1])] {
				t.Errorf("opencode.go queries table %q, which is not on the allow-list", m[1])
			}
		}
	}
	if seen < 4 {
		t.Fatalf("the allow-list check found only %d table references; the test is not looking at the SQL", seen)
	}
	// 2. running the adapter leaves the database byte-for-byte unchanged
	a, dbPath := install(t, "1.18/schema.sql", "1.18/basic.sql")
	before := fileHash(t, dbPath)
	refs, _ := list(t, a)
	for _, r := range refs {
		load(t, a, r)
	}
	if after := fileHash(t, dbPath); after != before {
		t.Error("the adapter modified the OpenCode database")
	}
}

func fileHash(t *testing.T, p string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func TestUnexpectedLayoutAndMissingDB(t *testing.T) {
	// no database at all: nothing to list, no error
	empty := opencode.New(opencode.Options{DataDir: t.TempDir()})
	if refs, warns := list(t, empty); len(refs) != 0 || len(warns) != 0 {
		t.Errorf("missing db: %v %v", refs, warns)
	}
	if d, _ := empty.Detect(context.Background()); d.Installed {
		t.Error("no db is not an install")
	}
	// a database with a different layout is skipped with a warning, not a crash
	dir := t.TempDir()
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(dir, "opencode.db"))
	_, _ = db.ExecContext(context.Background(), `CREATE TABLE session(id TEXT)`)
	_ = db.Close()
	a := opencode.New(opencode.Options{DataDir: dir})
	refs, warns := list(t, a)
	if len(refs) != 0 || len(warns) != 1 || !adapters.IsSkip(warns[0]) {
		t.Errorf("odd layout: %v %v", refs, warns)
	}
	// a garbage file is not a database
	dir2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir2, "opencode.db"), []byte("this is not sqlite"), 0o600)
	if _, warns := list(t, opencode.New(opencode.Options{DataDir: dir2})); len(warns) != 1 {
		t.Errorf("garbage db: %v", warns)
	}
	// legacy JSON layout is called out
	dir3 := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir3, "storage", "session"), 0o755)
	d, _ := opencode.New(opencode.Options{DataDir: dir3}).Detect(context.Background())
	if d.Installed || !strings.Contains(strings.Join(d.Notes, "|"), "legacy") {
		t.Errorf("legacy layout: %+v", d)
	}
}

func TestMalformedRowsAreSkipped(t *testing.T) {
	a, dbPath := install(t, "1.18/schema.sql", "1.18/basic.sql")
	db, _ := sql.Open("sqlite", "file:"+dbPath)
	_, err := db.ExecContext(context.Background(), `INSERT INTO message(id,session_id,time_created,time_updated,data) VALUES('msg_bad','ses_main',9,9,'not json');
		INSERT INTO part(id,message_id,session_id,time_created,time_updated,data) VALUES('prt_bad','msg_0001','ses_main',9,9,'{{');`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	refs, _ := list(t, a)
	s := load(t, a, refs["ses_main"])
	if len(s.Warnings) == 0 || len(s.Turns) != 2 {
		t.Errorf("malformed rows must warn and the rest must still parse: %v / %d turns", s.Warnings, len(s.Turns))
	}
}
