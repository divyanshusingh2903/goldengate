package claudeai_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/OWNER/goldengate/internal/adapters"
	"github.com/OWNER/goldengate/internal/adapters/claudeai"
	"github.com/OWNER/goldengate/internal/adapters/export/exporttest"
	"github.com/OWNER/goldengate/internal/model"
	"github.com/OWNER/goldengate/internal/workitems"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixture = "../../../testdata/claude-ai"

func list(t *testing.T, paths ...string) (map[string]model.SessionRef, []error) {
	t.Helper()
	refs, warns := map[string]model.SessionRef{}, []error(nil)
	for ref, err := range claudeai.New(paths).ListSessions(context.Background(), adapters.ListOpts{}) {
		if err != nil {
			warns = append(warns, err)
			continue
		}
		refs[ref.ID] = ref
	}
	return refs, warns
}

func TestProbeAndListing(t *testing.T) {
	zipPath := exporttest.ZipDir(t, filepath.Join(fixture, "export"))
	sum, err := claudeai.New(nil).Probe(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Conversations != 3 || sum.Skipped != 2 {
		t.Errorf("summary: %+v", sum)
	}
	want := map[string]int{"claude-ai:Demo Project": 1, "claude-ai:Chats": 1, "claude-ai:Side _project_ _x_": 1}
	if len(sum.Buckets) != len(want) {
		t.Fatalf("buckets: %v", sum.Buckets)
	}
	for k, v := range want {
		if sum.Buckets[k] != v {
			t.Errorf("bucket %q: %v", k, sum.Buckets)
		}
	}
	refs, warns := list(t, zipPath)
	if len(refs) != 3 || len(warns) != 1 || !adapters.IsSkip(warns[0]) {
		t.Fatalf("refs=%d warns=%v", len(refs), warns)
	}
	r := refs["c0000000-0000-4000-8000-000000000001"]
	if r.Source != "claude-ai" || r.ProjectKey != "claude-ai:Demo Project" || r.SizeBytes != 4 || r.StartedAt.Year() != 2026 || !r.UpdatedAt.After(r.StartedAt) {
		t.Errorf("ref: %+v", r)
	}
	if refs["c0000000-0000-4000-8000-000000000003"].UpdatedAt.IsZero() {
		t.Error("a conversation without updated_at falls back to created_at")
	}
}

type view struct {
	Title string
	Tools map[string]int
	Turns []turn
}
type turn struct {
	Role, Text, Last string
	Tools            []tool `json:",omitempty"`
}
type tool struct{ Name, Kind, Input string }

func render(s *model.RawSession) view {
	v := view{Title: s.Title, Tools: s.ToolsUsed}
	for _, tn := range s.Turns {
		tv := turn{Role: tn.Role, Text: tn.Text, Last: tn.LastText}
		for _, c := range tn.Tools {
			tv.Tools = append(tv.Tools, tool{c.Name, string(c.Kind), string(c.Input)})
		}
		v.Turns = append(v.Turns, tv)
	}
	return v
}

func golden(t *testing.T, name string, got any) {
	t.Helper()
	b, _ := json.MarshalIndent(got, "", "  ")
	b = append(b, '\n')
	p := filepath.Join(fixture, name)
	if *update {
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("missing golden (run -update): %v", err)
	}
	if string(want) != string(b) {
		t.Errorf("golden mismatch (run -update and review)\n%s", b)
	}
}

func load(t *testing.T, paths ...string) *model.RawSession {
	refs, _ := list(t, paths...)
	s, err := claudeai.New(paths).Load(context.Background(), refs["c0000000-0000-4000-8000-000000000001"])
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLoadGoldenAndHygiene(t *testing.T) {
	s := load(t, exporttest.ZipDir(t, filepath.Join(fixture, "export")))
	golden(t, "export.c1.golden.json", render(s))
	blob, _ := json.Marshal(render(s))
	for _, bad := range []string{"SECRET-", "PRIVATE", "someone@example.invalid", "Someone Real"} {
		if strings.Contains(string(blob), bad) {
			t.Errorf("%q leaked into the session", bad)
		}
	}
	if s.Title != "Fix the login redirect" || len(s.Turns) != 4 || s.Turns[3].LastText != "Updated." {
		t.Errorf("turns/title: %d %q", len(s.Turns), s.Title)
	}
}

func TestWorkItemsFromChat(t *testing.T) {
	items := workitems.Extract(load(t, exporttest.ZipDir(t, filepath.Join(fixture, "export"))), workitems.Options{})
	status := map[string]string{}
	for _, it := range items {
		status[it.Kind+" "+it.Key] = it.Status
	}
	want := map[string]string{
		"artifact artifact:art-1":                 "updated", // created, then updated
		"artifact artifact:upload:notes.md":       "created",
		"artifact artifact:upload:screenshot.png": "created",
		"pull_request example/demo#7":             "mentioned", // text only: a chat cannot prove anything
		"commit example/demo@abc1234":             "mentioned",
	}
	for k, v := range want {
		if status[k] != v {
			t.Errorf("%s: got %q want %q (%v)", k, status[k], v, status)
		}
	}
	if len(items) != len(want) {
		t.Errorf("unexpected extra items (a secret-named upload must be dropped): %v", status)
	}
	all, _ := json.Marshal(items)
	if strings.Contains(string(all), "SECRET") {
		t.Error("content reached work items")
	}
	for _, it := range items {
		if it.Kind == "artifact" && it.Evidence[0].Via != "harness_event" {
			t.Errorf("artifact evidence: %+v", it.Evidence)
		}
	}
}

func TestOnlyKnownFilesAreEverOpened(t *testing.T) {
	src, _ := os.ReadFile("claudeai.go")
	allowed := map[string]bool{"conversations.json": true, "projects.json": true}
	for _, m := range regexp.MustCompile(`"([A-Za-z0-9_./-]+\.(?:json|html|txt|csv))"`).FindAllStringSubmatch(string(src), -1) {
		if !allowed[m[1]] {
			t.Errorf("claudeai.go names %q: only conversations.json and projects.json may be opened", m[1])
		}
	}
}

func TestSameConversationInTwoExportsListedOnce(t *testing.T) {
	older := exporttest.ZipDir(t, filepath.Join(fixture, "export"))
	b, _ := os.ReadFile(filepath.Join(fixture, "export", "conversations.json"))
	newer := exporttest.ZipDirAs(t, filepath.Join(fixture, "export"), map[string][]byte{
		"conversations.json": bytes.ReplaceAll(b, []byte("2026-01-05T09:30:00.000000Z"), []byte("2026-02-01T00:00:00.000000Z")),
	})
	refs, _ := list(t, older, newer)
	if len(refs) != 3 {
		t.Fatalf("duplicates listed: %d", len(refs))
	}
	if r := refs["c0000000-0000-4000-8000-000000000001"]; r.Path != newer || r.UpdatedAt.Month() != 2 {
		t.Errorf("the export that saw it last must win: %+v", r)
	}
}

func TestBadArchivesAreSkippedWithWarnings(t *testing.T) {
	good := exporttest.ZipDir(t, filepath.Join(fixture, "export"))
	junk := filepath.Join(t.TempDir(), "junk.zip")
	_ = os.WriteFile(junk, []byte("not a zip"), 0o600)
	wrongProduct := exporttest.ZipDirAs(t, filepath.Join(fixture, "export"), map[string][]byte{"conversations.json": []byte(`{"not":"an array"}`)})
	var hostile bytes.Buffer
	zw := zip.NewWriter(&hostile)
	w, _ := zw.Create("../../evil")
	_, _ = w.Write([]byte("x"))
	_ = zw.Close()
	hostilePath := filepath.Join(t.TempDir(), "hostile.zip")
	_ = os.WriteFile(hostilePath, hostile.Bytes(), 0o600)

	refs, warns := list(t, filepath.Join(t.TempDir(), "missing.zip"), junk, wrongProduct, hostilePath, good)
	if len(refs) != 3 {
		t.Errorf("the good archive must still be served: %d", len(refs))
	}
	if len(warns) < 5 {
		t.Errorf("each bad archive must produce a warning: %v", warns)
	}
	for _, p := range []string{junk, wrongProduct, hostilePath} {
		if _, err := claudeai.New(nil).Probe(p); err == nil {
			t.Errorf("import must refuse %s", filepath.Base(p))
		}
	}
	d, _ := claudeai.New([]string{filepath.Join(t.TempDir(), "gone.zip")}).Detect(context.Background())
	if d.Installed || len(d.Notes) == 0 {
		t.Errorf("a missing archive is not an install: %+v", d)
	}
}

func FuzzLoad(f *testing.F) {
	seed, _ := os.ReadFile(filepath.Join(fixture, "export", "conversations.json"))
	f.Add(seed)
	f.Add([]byte(`[{"uuid":"x","chat_messages":[{"sender":"human","content":[{"type":"tool_use"}]}]}]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		p := exporttest.ZipDirAs(t, filepath.Join(fixture, "export"), map[string][]byte{"conversations.json": data})
		a := claudeai.New([]string{p})
		for ref, err := range a.ListSessions(context.Background(), adapters.ListOpts{}) {
			if err == nil {
				_, _ = a.Load(context.Background(), ref)
			}
		}
	})
}
