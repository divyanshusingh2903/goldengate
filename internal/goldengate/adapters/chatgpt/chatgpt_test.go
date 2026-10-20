package chatgpt_test

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/chatgpt"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/export/exporttest"
	"github.com/divyanshusingh2903/goldengate/internal/model"
	"github.com/divyanshusingh2903/goldengate/internal/utils/workitems"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixture = "../../../../testdata/chatgpt"

func list(t *testing.T, paths ...string) (map[string]model.SessionRef, []error) {
	t.Helper()
	refs, warns := map[string]model.SessionRef{}, []error(nil)
	for ref, err := range chatgpt.New(paths).ListSessions(context.Background(), adapters.ListOpts{}) {
		if err != nil {
			warns = append(warns, err)
			continue
		}
		refs[ref.ID] = ref
	}
	return refs, warns
}

func loadByID(t *testing.T, id string, paths ...string) *model.RawSession {
	t.Helper()
	refs, _ := list(t, paths...)
	ref, ok := refs[id]
	if !ok {
		t.Fatalf("no conversation %s in %v", id, refs)
	}
	s, err := chatgpt.New(paths).Load(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestProbeAndListing(t *testing.T) {
	zipPath := exporttest.ZipDir(t, filepath.Join(fixture, "export"))
	sum, err := chatgpt.New(nil).Probe(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Conversations != 4 || sum.Skipped != 2 {
		t.Errorf("summary: %+v", sum)
	}
	want := map[string]int{"chatgpt:Chats": 2, "chatgpt:project-01234567": 1, "chatgpt:gpt-abcdef12": 1}
	for k, v := range want {
		if sum.Buckets[k] != v {
			t.Errorf("bucket %q: %v", k, sum.Buckets)
		}
	}
	refs, warns := list(t, zipPath)
	if len(refs) != 4 || len(warns) != 1 {
		t.Fatalf("refs=%d warns=%v", len(refs), warns)
	}
	r := refs["gpt-conv-0001"]
	if r.Source != "chatgpt" || r.ProjectKey != "chatgpt:Chats" || r.StartedAt.Year() != 2026 || !r.UpdatedAt.After(r.StartedAt) {
		t.Errorf("ref: %+v", r)
	}
	if refs["gpt-conv-0003"].StartedAt.IsZero() {
		t.Error("timestamps given as numeric strings must still parse")
	}
}

type view struct {
	Title string
	Tools map[string]int
	Turns []turn
}
type turn struct{ Role, Text, Last string }

func render(s *model.RawSession) view {
	v := view{Title: s.Title, Tools: s.ToolsUsed}
	for _, tn := range s.Turns {
		v.Turns = append(v.Turns, turn{tn.Role, tn.Text, tn.LastText})
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

func TestActiveBranchOnlyAndHygiene(t *testing.T) {
	s := loadByID(t, "gpt-conv-0001", exporttest.ZipDir(t, filepath.Join(fixture, "export")))
	golden(t, "export.conv1.golden.json", render(s))
	blob, _ := json.Marshal(render(s))
	for _, bad := range []string{"OLD BRANCH", "Original wording", "HIDDEN", "PRIVATE", "SECRET", "INJECTED", "file-SECRET", "someone@example.invalid"} {
		if strings.Contains(string(blob), bad) {
			t.Errorf("%q must not reach the session", bad)
		}
	}
	if len(s.Turns) != 4 || s.Turns[0].Role != "user" || !strings.Contains(s.Turns[0].Text, "How do I fix the redirect") {
		t.Fatalf("the edited branch is the conversation: %d turns, first %q", len(s.Turns), s.Turns[0].Text)
	}
	if s.Turns[2].Text != "What does this screenshot show?" || s.Turns[3].LastText != "It shows the login page." {
		t.Errorf("multimodal text and the final answer: %+v", s.Turns[2:])
	}
	if s.ToolsUsed["python"] != 1 {
		t.Errorf("tool calls are counted, not transcribed: %v", s.ToolsUsed)
	}
}

func TestMissingCurrentNodeAndCycles(t *testing.T) {
	zipPath := exporttest.ZipDir(t, filepath.Join(fixture, "export"))
	s := loadByID(t, "gpt-conv-0003", zipPath)
	if len(s.Turns) != 2 || s.Turns[1].Text != "picked the newest leaf" || s.Title != "" {
		t.Errorf("no current_node: %+v", s.Turns)
	}
	c := loadByID(t, "gpt-conv-0004", zipPath) // c1 <-> c2 parent loop: must terminate
	if len(c.Turns) > 2 {
		t.Errorf("cycle walked repeatedly: %d turns", len(c.Turns))
	}
}

func TestShardedExports(t *testing.T) {
	zipPath := exporttest.ZipDir(t, filepath.Join(fixture, "sharded"))
	refs, warns := list(t, zipPath)
	if len(refs) != 2 || len(warns) != 0 {
		t.Fatalf("both shards must be read: %v %v", refs, warns)
	}
	if s := loadByID(t, "shard-b", zipPath); len(s.Turns) != 2 || s.Turns[0].Text != "question b" {
		t.Errorf("%+v", s.Turns)
	}
}

func TestWorkItemsFromChatAreMentionsOnly(t *testing.T) {
	items := workitems.Extract(loadByID(t, "gpt-conv-0001", exporttest.ZipDir(t, filepath.Join(fixture, "export"))), workitems.Options{})
	status := map[string]string{}
	for _, it := range items {
		status[it.Kind+" "+it.Key] = it.Status
	}
	if status["pull_request example/demo#7"] != "mentioned" || status["commit example/demo@abc1234"] != "mentioned" || len(items) != 2 {
		t.Errorf("a chat can only mention: %v", status)
	}
}

func TestOnlyConversationFilesAreEverOpened(t *testing.T) {
	src, _ := os.ReadFile("chatgpt.go")
	for _, m := range regexp.MustCompile("[\"`]([^\"`]*\\.(?:json|html|txt|csv))[\"`]").FindAllStringSubmatch(string(src), -1) {
		if m[1] != "conversations.json" && !strings.HasPrefix(m[1], "^conversations-") {
			t.Errorf("chatgpt.go names %q: only conversation files may be opened", m[1])
		}
	}
	// and behaviorally: the decoys hold secrets that must never surface anywhere
	zipPath := exporttest.ZipDir(t, filepath.Join(fixture, "export"))
	refs, _ := list(t, zipPath)
	a := chatgpt.New([]string{zipPath})
	for _, ref := range refs {
		s, err := a.Load(context.Background(), ref)
		if err != nil {
			continue
		}
		blob, _ := json.Marshal(render(s))
		if strings.Contains(string(blob), "SECRET-HTML") || strings.Contains(string(blob), "SECRET-FEEDBACK") {
			t.Error("a file other than the conversations leaked")
		}
	}
}

func TestWrongProductArchive(t *testing.T) {
	// a claude.ai export has no ChatGPT-style mapping: its conversations are skipped, not misread
	claude := exporttest.ZipDir(t, filepath.Join("../../../../testdata/claude-ai", "export"))
	sum, err := chatgpt.New(nil).Probe(claude)
	if err == nil && sum.Conversations > 0 {
		t.Errorf("claude.ai conversations were accepted as ChatGPT ones: %+v", sum)
	}
}

func FuzzLoad(f *testing.F) {
	seed, _ := os.ReadFile(filepath.Join(fixture, "export", "conversations.json"))
	f.Add(seed)
	f.Add([]byte(`[{"id":"x","current_node":"a","mapping":{"a":{"parent":"a","message":{"author":{"role":"user"},"content":{"parts":[1,{"x":2},null]}}}}}]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		p := exporttest.ZipDirAs(t, filepath.Join(fixture, "export"), map[string][]byte{"conversations.json": data})
		a := chatgpt.New([]string{p})
		for ref, err := range a.ListSessions(context.Background(), adapters.ListOpts{}) {
			if err == nil {
				_, _ = a.Load(context.Background(), ref)
			}
		}
	})
}
