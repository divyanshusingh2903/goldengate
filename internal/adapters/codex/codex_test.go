package codex_test

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
	"github.com/OWNER/goldengate/internal/adapters/codex"
	"github.com/OWNER/goldengate/internal/model"
	"github.com/OWNER/goldengate/internal/workitems"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixtures = "../../../testdata/codex"

// install copies a fixture into a fake CODEX_HOME laid out like the real one.
func install(t *testing.T, fixture string) (*codex.Adapter, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtures, fixture))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "01", "05")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "rollout-2026-01-05T09-00-00-00000000-0000-4000-8000-000000000001.jsonl")
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return codex.New(codex.Options{CodexHome: home}), dst
}

func list(t *testing.T, a *codex.Adapter) (refs []model.SessionRef, warns []error) {
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

func load(t *testing.T, fixture string) *model.RawSession {
	t.Helper()
	a, _ := install(t, fixture)
	refs, warns := list(t, a)
	if len(refs) != 1 {
		t.Fatalf("refs=%d warns=%v", len(refs), warns)
	}
	s, err := a.Load(context.Background(), refs[0])
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type view struct {
	Project, Branch, Remote, Version string
	ToolsUsed                        map[string]int
	FilesTouched                     []string
	Warnings                         []string
	Turns                            []turnView
}
type turnView struct {
	Role, Text string
	Tools      []toolView `json:",omitempty"`
}
type toolView struct {
	Name, Kind, Command, Path string
	Created, Known, OK        bool
	Exit                      *int
}

func render(s *model.RawSession) view {
	v := view{Project: s.Ref.ProjectKey, Branch: s.GitBranch, Remote: s.GitRemote, Version: s.SourceVersion, ToolsUsed: s.ToolsUsed, FilesTouched: s.FilesTouched, Warnings: s.Warnings}
	for _, t := range s.Turns {
		tv := turnView{Role: t.Role, Text: t.Text}
		for _, c := range t.Tools {
			tv.Tools = append(tv.Tools, toolView{c.Name, string(c.Kind), c.Command, c.Path, c.Created, c.Result.Known, c.Result.OK, c.Result.ExitCode})
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

func TestBasicGolden(t *testing.T) {
	s := load(t, "0.15x/basic.jsonl")
	if s.Ref.ProjectKey != "/home/user/projects/demo" || s.Ref.Source != codex.Name {
		t.Fatalf("ref: %+v", s.Ref)
	}
	if len(s.Warnings) != 0 {
		t.Fatalf("warnings: %v", s.Warnings)
	}
	golden(t, "0.15x/basic.golden.json", render(s))
}

func TestWorkitemsSessionGolden(t *testing.T) {
	s := load(t, "0.15x/workitems.jsonl")
	golden(t, "0.15x/workitems.session.golden.json", render(s))
	// conversation hygiene
	var all strings.Builder
	for _, tn := range s.Turns {
		all.WriteString(tn.Text)
	}
	for _, bad := range []string{"SYSTEM INSTRUCTIONS", "environment_context", "hunter2token"} {
		if strings.Contains(all.String(), bad) || strings.Contains(s.GitRemote, bad) {
			t.Errorf("%q leaked into the session", bad)
		}
	}
	if s.GitRemote != "https://github.com/example/demo.git" || s.GitBranch != "main" {
		t.Errorf("git: %q %q", s.GitRemote, s.GitBranch)
	}
	if len(s.Turns) != 2 || s.Turns[0].Role != "user" || s.Turns[1].LastText != "All done." {
		t.Errorf("turns must merge consecutive assistant output: %d", len(s.Turns))
	}
}

func TestEvidenceSemantics(t *testing.T) {
	items := workitems.Extract(load(t, "0.15x/workitems.jsonl"), workitems.Options{})
	status := map[string]string{}
	for _, it := range items {
		status[it.Kind+" "+it.Key] = it.Status
	}
	want := map[string]string{
		"commit example/demo@abc1234":  "created",   // exit 0 + commit line
		"commit example/demo@def5678":  "created",   // legacy `shell` form with JSON metadata.exit_code
		"commit example/demo@deadbee1": "mentioned", // text only
		"pull_request example/demo#7":  "merged",
		"pull_request example/demo#12": "mentioned",
		"file src/new.go":              "created",
		"file src/a.go":                "updated",
	}
	for k, v := range want {
		if status[k] != v {
			t.Errorf("%s: got %q want %q", k, status[k], v)
		}
	}
	for _, absent := range []string{"commit example/demo@9999999", "file src/bad.go", "file .env"} {
		if _, ok := status[absent]; ok {
			t.Errorf("%s must not be a work item (failed, hidden in code mode, or secret)", absent)
		}
	}
	if len(items) != len(want) {
		t.Errorf("unexpected extra items: %v", status)
	}
}

func TestStillRunningIsNotEvidence(t *testing.T) {
	s := load(t, "0.15x/workitems.jsonl")
	for _, tn := range s.Turns {
		for _, c := range tn.Tools {
			if strings.Contains(c.Command, "commit -m slow") && c.Result.Known {
				t.Error("a process that has not exited has no known outcome")
			}
		}
	}
}

func TestUnknownShapesDoNotCrash(t *testing.T) {
	s := load(t, "unknown_garbage.jsonl")
	if len(s.Warnings) == 0 {
		t.Error("expected warnings")
	}
	if len(s.Turns) == 0 || !strings.Contains(s.Turns[len(s.Turns)-1].Text, "real prompt") {
		t.Errorf("valid events after the noise must still parse: %+v", s.Turns)
	}
	if !strings.Contains(strings.Join(s.Warnings, "|"), "untested Codex version 9.9.9") {
		t.Errorf("version warning: %v", s.Warnings)
	}
}

func TestMissingSessionMetaIsSkipped(t *testing.T) {
	a, _ := install(t, "unknown_nometa.jsonl")
	refs, warns := list(t, a)
	if len(refs) != 0 || len(warns) != 1 || !adapters.IsSkip(warns[0]) {
		t.Fatalf("refs=%d warns=%v", len(refs), warns)
	}
}

func TestListingGuards(t *testing.T) {
	a, dst := install(t, "0.15x/basic.jsonl")
	// since filter
	n := 0
	for range a.ListSessions(context.Background(), adapters.ListOpts{Since: time.Now().Add(time.Hour)}) {
		n++
	}
	if n != 0 {
		t.Errorf("since filter: %d", n)
	}
	// symlinked rollouts and stray files are ignored
	link := filepath.Join(filepath.Dir(dst), "rollout-2026-01-05T10-00-00-00000000-0000-4000-8000-000000000002.jsonl")
	if err := os.Symlink(dst, link); err == nil {
		if refs, _ := list(t, a); len(refs) != 1 {
			t.Errorf("symlink listed: %d", len(refs))
		}
		if _, err := a.Load(context.Background(), model.SessionRef{ID: "x", Path: link}); !adapters.IsSkip(err) {
			t.Errorf("Load must refuse symlinks: %v", err)
		}
	}
	_ = os.WriteFile(filepath.Join(filepath.Dir(dst), "notes.jsonl"), []byte(`{"type":"session_meta","payload":{"cwd":"/x","id":"y"}}`+"\n"), 0o600)
	if refs, _ := list(t, a); len(refs) != 1 {
		t.Errorf("only rollout-*.jsonl may be read: %d", len(refs))
	}
	// size cap
	small := codex.New(codex.Options{CodexHome: filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(dst))))), MaxFileBytes: 10})
	if _, warns := list(t, small); len(warns) != 1 {
		t.Errorf("size cap must skip with a warning: %v", warns)
	}
}

func TestDetect(t *testing.T) {
	if d, _ := codex.New(codex.Options{CodexHome: t.TempDir()}).Detect(context.Background()); d.Installed {
		t.Error("empty home is not an install")
	}
	a, _ := install(t, "0.15x/basic.jsonl")
	if d, _ := a.Detect(context.Background()); !d.Installed {
		t.Error("expected installed")
	}
}

func FuzzLoad(f *testing.F) {
	seed, _ := os.ReadFile(filepath.Join(fixtures, "0.15x/workitems.jsonl"))
	f.Add(seed)
	f.Add([]byte(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","input":"*** Add File: ","call_id":"c"}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		p := filepath.Join(t.TempDir(), "s.jsonl")
		if os.WriteFile(p, data, 0o600) != nil {
			t.Skip()
		}
		_, _ = codex.New(codex.Options{CodexHome: t.TempDir()}).Load(context.Background(), model.SessionRef{ID: "s", Path: p, ProjectKey: "/p"})
	})
}
