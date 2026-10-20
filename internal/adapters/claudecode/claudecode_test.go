package claudecode

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OWNER/goldengate/internal/adapters"
	"github.com/OWNER/goldengate/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixtures = "../../../testdata/claude-code"

// install copies a fixture into a fake config dir and returns the adapter.
func install(t *testing.T, fixture, project string) (*Adapter, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtures, fixture))
	if err != nil {
		t.Fatal(err)
	}
	cfg := t.TempDir()
	dir := filepath.Join(cfg, "projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "11111111-1111-4111-8111-111111111111.jsonl")
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return New(Options{ConfigDir: cfg}), dst
}

func list(t *testing.T, a *Adapter) (refs []model.SessionRef, warns []error) {
	t.Helper()
	for ref, err := range a.ListSessions(context.Background(), adapters.ListOpts{}) {
		if err != nil {
			warns = append(warns, err)
			continue
		}
		refs = append(refs, ref)
	}
	return
}

// view is a stable, human-reviewable rendering of a RawSession.
type view struct {
	ID           string
	Project      string
	Title        string
	Branch       string
	Version      string
	ToolsUsed    map[string]int
	FilesTouched []string
	Events       []model.HarnessEvent
	Warnings     []string
	Turns        []turnView
}
type turnView struct {
	Role  string
	Text  string
	Tools []toolView `json:",omitempty"`
}
type toolView struct {
	Name, Kind, Command, Path string
	Created                   bool
	Known, OK                 bool
	Exit                      *int
}

func render(s *model.RawSession) view {
	v := view{ID: s.Ref.ID, Project: s.Ref.ProjectKey, Title: s.Title, Branch: s.GitBranch, Version: s.SourceVersion,
		ToolsUsed: s.ToolsUsed, FilesTouched: s.FilesTouched, Events: s.Events, Warnings: s.Warnings}
	for _, t := range s.Turns {
		tv := turnView{Role: t.Role, Text: t.Text}
		for _, c := range t.Tools {
			tv.Tools = append(tv.Tools, toolView{c.Name, string(c.Kind), c.Command, c.Path, c.Created, c.Result.Known, c.Result.OK, c.Result.ExitCode})
		}
		v.Turns = append(v.Turns, tv)
	}
	for i := range v.Events {
		v.Events[i].Time = v.Events[i].Time.UTC()
	}
	return v
}

func golden(t *testing.T, name string, got any) {
	t.Helper()
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
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
		t.Errorf("golden mismatch for %s; run with -update and review the diff\n--- got\n%s", name, b)
	}
}

func TestBasicGolden(t *testing.T) {
	a, _ := install(t, "2.1/basic.jsonl", "-home-user-projects-demo")
	refs, warns := list(t, a)
	if len(warns) != 0 || len(refs) != 1 {
		t.Fatalf("refs=%d warns=%v", len(refs), warns)
	}
	if refs[0].ProjectKey != "/home/user/projects/demo" || refs[0].Source != Name {
		t.Fatalf("bad ref: %+v", refs[0])
	}
	s, err := a.Load(context.Background(), refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", s.Warnings)
	}
	r := render(s)
	r.ID = "<id>"
	golden(t, "2.1/basic.golden.json", r)
}

func TestWorkitemsFixtureShape(t *testing.T) {
	a, _ := install(t, "2.1/workitems.jsonl", "-home-user-projects-demo")
	refs, _ := list(t, a)
	s, err := a.Load(context.Background(), refs[0])
	if err != nil {
		t.Fatal(err)
	}
	r := render(s)
	r.ID = "<id>"
	golden(t, "2.1/workitems.session.golden.json", r)
	// failed commit must be recorded as failed, not just absent
	var failed bool
	for _, turn := range s.Turns {
		for _, c := range turn.Tools {
			if strings.Contains(c.Command, `-m "oops"`) {
				failed = c.Result.Known && !c.Result.OK && c.Result.ExitCode != nil && *c.Result.ExitCode == 1
			}
		}
	}
	if !failed {
		t.Error("failed git commit not recorded as failed with exit code 1")
	}
}

func TestUnknownShapesDoNotCrash(t *testing.T) {
	a, _ := install(t, "unknown/garbage.jsonl", "-p")
	refs, warns := list(t, a)
	if len(refs) != 1 {
		t.Fatalf("garbage file has a cwd in line 2 and must be listed; refs=%d warns=%v", len(refs), warns)
	}
	s, err := a.Load(context.Background(), refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Warnings) == 0 {
		t.Error("expected warnings for unknown shapes")
	}
	if len(s.Turns) == 0 || !strings.Contains(s.Turns[len(s.Turns)-1].Text, "real prompt") {
		t.Errorf("valid events after noise must still be parsed: %+v", s.Turns)
	}
	joined := strings.Join(s.Warnings, "|")
	if !strings.Contains(joined, "untested Claude Code version 9.9.9") {
		t.Errorf("version warning missing: %s", joined)
	}
}

func TestMissingCwdIsSkippedWithWarning(t *testing.T) {
	a, _ := install(t, "unknown/nocwd.jsonl", "-p")
	refs, warns := list(t, a)
	if len(refs) != 0 || len(warns) != 1 || !adapters.IsSkip(warns[0]) {
		t.Fatalf("refs=%d warns=%v", len(refs), warns)
	}
}

func TestSinceFilterAndSizeCap(t *testing.T) {
	a, _ := install(t, "2.1/basic.jsonl", "-p")
	n := 0
	for range a.ListSessions(context.Background(), adapters.ListOpts{Since: timeFar()}) {
		n++
	}
	if n != 0 {
		t.Errorf("since filter ignored: %d", n)
	}
	small := New(Options{ConfigDir: filepath.Dir(filepath.Dir(filepath.Dir(mustFirst(t, a)))), MaxFileBytes: 10})
	_, warns := list(t, small)
	if len(warns) != 1 {
		t.Errorf("size cap must skip with warning, got %v", warns)
	}
}

func mustFirst(t *testing.T, a *Adapter) string {
	t.Helper()
	refs, _ := list(t, a)
	return refs[0].Path
}

func TestSymlinkedSessionIsIgnored(t *testing.T) {
	a, dst := install(t, "2.1/basic.jsonl", "-p")
	link := filepath.Join(filepath.Dir(dst), "22222222-2222-4222-8222-222222222222.jsonl")
	if err := os.Symlink(dst, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	refs, _ := list(t, a)
	if len(refs) != 1 {
		t.Errorf("symlinked session must be ignored, got %d refs", len(refs))
	}
	if _, err := a.Load(context.Background(), model.SessionRef{ID: "x", Path: link}); !adapters.IsSkip(err) {
		t.Errorf("Load must refuse symlinks, got %v", err)
	}
}

func TestOnlyTopLevelJSONLRead(t *testing.T) {
	a, dst := install(t, "2.1/basic.jsonl", "-p")
	sub := filepath.Join(filepath.Dir(dst), "11111111-1111-4111-8111-111111111111", "subagents")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(sub, "agent.jsonl"), []byte(`{"cwd":"/x"}`+"\n"), 0o600)
	_ = os.MkdirAll(filepath.Join(filepath.Dir(dst), "memory"), 0o755)
	_ = os.WriteFile(filepath.Join(filepath.Dir(dst), "memory", "m.jsonl"), []byte(`{"cwd":"/y"}`+"\n"), 0o600)
	if refs, _ := list(t, a); len(refs) != 1 {
		t.Errorf("nested files must not be listed: %d", len(refs))
	}
}

func TestDetect(t *testing.T) {
	d, _ := New(Options{ConfigDir: t.TempDir()}).Detect(context.Background())
	if d.Installed {
		t.Error("empty config dir is not an install")
	}
	a, _ := install(t, "2.1/basic.jsonl", "-p")
	if d, _ := a.Detect(context.Background()); !d.Installed {
		t.Error("expected installed")
	}
}

func FuzzLoad(f *testing.F) {
	seed, _ := os.ReadFile(filepath.Join(fixtures, "2.1/workitems.jsonl"))
	f.Add(seed)
	f.Add([]byte(`{"type":"user","message":{"content":[{"type":"tool_result"}]}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		p := filepath.Join(dir, "s.jsonl")
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Skip()
		}
		a := New(Options{ConfigDir: dir})
		_, _ = a.Load(context.Background(), model.SessionRef{ID: "s", Path: p, ProjectKey: "/p"})
	})
}

func timeFar() (t time.Time) { return time.Now().Add(24 * time.Hour) }
