package summarize

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/model"
)

func fakeSecret() string { return "AKIA" + "IOSFODNN7EXAMPLE" }

func session() *model.RawSession {
	t0 := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	exit0 := 0
	return &model.RawSession{
		Ref: model.SessionRef{Source: "claude-code", ID: "s1", ProjectKey: "/home/u/code/app", StartedAt: t0, UpdatedAt: t0.Add(30 * time.Minute)},
		Turns: []model.Turn{
			{Role: "user", Text: "Fix the login bug. My key is " + fakeSecret() + " and the file is /home/u/code/app/auth.go\nsecond line", Time: t0},
			{Role: "assistant", Text: "working", LastText: "Done. Wrote /home/u/code/other/secret.txt and used " + fakeSecret(), Time: t0.Add(time.Minute), Tools: []model.ToolCall{
				{Name: "Bash", Kind: model.ToolShell, Command: "FOO=bar git commit -m 'super secret msg' && go test ./...", Result: model.ToolResult{Known: true, OK: true, ExitCode: &exit0, Text: "[main abc1234] feat: x"}},
				{Name: "Write", Kind: model.ToolFileWrite, Path: "/home/u/code/app/auth.go", Created: true, Result: model.ToolResult{Known: true, OK: true}},
			}},
		},
		FilesTouched: []string{"auth.go", ".env", "keys/id_rsa", "/etc/passwd"},
		ToolsUsed:    map[string]int{"Bash": 3, "mcp__uuid-123__save_issue": 1},
		GitBranch:    "main",
	}
}

func TestSummaryLevel(t *testing.T) {
	p, err := Build(session(), Options{Home: "/home/u"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p)
	out := string(b)
	for _, bad := range []string{fakeSecret(), "IOSFODNN7", "/home/u/code/other", "secret.txt", "super secret msg", ".env", "id_rsa", "/etc/passwd", "uuid-123"} {
		if strings.Contains(out, bad) {
			t.Errorf("payload contains %q: %s", bad, out)
		}
	}
	if p.Project != "~/code/app" {
		t.Errorf("project not scrubbed: %s", p.Project)
	}
	if p.Redactions["aws-access-key"] == 0 || p.Redactions["path"] == 0 {
		t.Errorf("redaction counts missing: %v", p.Redactions)
	}
	if got := strings.Join(p.Summary.Commands, ","); got != "git commit,go test" {
		t.Errorf("commands must be names only, got %q", got)
	}
	if got := strings.Join(p.Summary.Outcomes, ", "); got != "tests passed, commit made" {
		t.Errorf("outcomes: %q", got)
	}
	if _, ok := p.Summary.ToolsUsed["mcp:save_issue"]; !ok {
		t.Errorf("mcp tool name not normalized: %v", p.Summary.ToolsUsed)
	}
	if p.Summary.TurnCount != 1 || p.Summary.DurationSec != 60 {
		t.Errorf("turns=%d dur=%d", p.Summary.TurnCount, p.Summary.DurationSec)
	}
	if len(p.Summary.FilesTouched) != 1 || p.Summary.FilesTouched[0] != "auth.go" {
		t.Errorf("files: %v", p.Summary.FilesTouched)
	}
	if strings.Contains(p.Summary.Title, "\n") || strings.Contains(p.Summary.Title, "second line") {
		t.Errorf("title must be the first line only: %q", p.Summary.Title)
	}
}

func TestTitlesOnlyCarriesNothingElse(t *testing.T) {
	p, err := Build(session(), Options{Level: model.LevelTitlesOnly, Home: "/home/u"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Summary != nil || len(p.WorkItems) != 0 || p.TitleOnly == "" {
		t.Fatalf("titles-only leaked more: %+v", p)
	}
	if strings.Contains(p.TitleOnly, fakeSecret()) {
		t.Error("secret in title")
	}
}

func TestFullLevelRefused(t *testing.T) {
	if _, err := Build(session(), Options{Level: model.LevelFull}); !errors.Is(err, ErrLevelUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestSummarySizeCap(t *testing.T) {
	s := session()
	s.FilesTouched = nil
	for i := 0; i < 100; i++ {
		s.FilesTouched = append(s.FilesTouched, strings.Repeat("d", 60)+"/f"+string(rune('a'+i%26))+strings.Repeat("x", i)+".go")
	}
	s.Turns[0].Text = strings.Repeat("goal words ", 500)
	s.Turns[1].LastText = strings.Repeat("closing words ", 500)
	p, err := Build(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(p.Summary)
	if len(b) > MaxSummaryBytes {
		t.Errorf("summary is %d bytes, cap %d", len(b), MaxSummaryBytes)
	}
}

func TestHashStableAndSensitive(t *testing.T) {
	a, _ := Build(session(), Options{})
	b, _ := Build(session(), Options{})
	if a.ContentHash == "" || a.ContentHash != b.ContentHash {
		t.Fatal("hash must be deterministic")
	}
	s := session()
	s.Turns[0].Text = "different goal"
	c, _ := Build(s, Options{})
	if c.ContentHash == a.ContentHash {
		t.Fatal("hash must change with content")
	}
	d, _ := Build(session(), Options{Level: model.LevelTitlesOnly})
	if d.ContentHash == a.ContentHash {
		t.Fatal("hash must change with level")
	}
}

func TestBuildDoesNotMutateSession(t *testing.T) {
	s := session()
	before, _ := json.Marshal(s)
	if _, err := Build(s, Options{}); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(s)
	if string(before) != string(after) {
		t.Fatal("Build mutated the session")
	}
}

func TestBadExtraPattern(t *testing.T) {
	if _, err := Build(session(), Options{ExtraRedactions: []string{"("}}); err == nil {
		t.Fatal("expected error")
	}
}

func TestWorkItemsMetadataOnly(t *testing.T) {
	p, _ := Build(session(), Options{})
	var found bool
	for _, it := range p.WorkItems {
		if it.Kind == "commit" && it.Status == "created" {
			found = true
		}
		if it.Kind == "file" && it.Key == ".env" {
			t.Error(".env in work items")
		}
	}
	if !found {
		t.Errorf("commit work item missing: %+v", p.WorkItems)
	}
}

func TestCommandNamesIgnoreHeredocsAndKeywords(t *testing.T) {
	s := session()
	s.Turns[1].Tools = []model.ToolCall{{Name: "Bash", Kind: model.ToolShell, Result: model.ToolResult{Known: true, OK: true},
		Command: "cat > a.go <<'EOF'\nif x {\n  return err\n}\nfunc main() { git commit }\nEOF\nFOO=1 go test ./... && git -C . status\nif true; then npm run build; fi\ngh pr create --title t"}}
	p, err := Build(s, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(p.Summary.Commands, ",")
	for _, bad := range []string{"if", "return", "func", "EOF", "err"} {
		if strings.Contains(","+got+",", ","+bad+",") {
			t.Errorf("keyword %q leaked into commands: %s", bad, got)
		}
	}
	for _, want := range []string{"cat", "go test", "git status", "npm run", "gh pr"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	outs := strings.Join(p.Summary.Outcomes, ", ")
	if strings.Contains(outs, "commit made") {
		t.Errorf("commit text inside a heredoc is not a commit: %q", outs)
	}
	if !strings.Contains(outs, "PR opened") || !strings.Contains(outs, "tests passed") {
		t.Errorf("outcomes: %q", outs)
	}
}
