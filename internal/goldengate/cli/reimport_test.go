package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/export/exporttest"
)

const claudeExport = "../../../testdata/claude-ai/export"

// newerClaudeExport is the fixture export as it would look later: one
// conversation continued, one new conversation, the rest untouched.
func newerClaudeExport(t *testing.T, mutate func(convs []any) []any) string {
	t.Helper()
	b, err := os.ReadFile(claudeExport + "/conversations.json")
	if err != nil {
		t.Fatal(err)
	}
	var convs []any
	if err := json.Unmarshal(b, &convs); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(mutate(convs))
	if err != nil {
		t.Fatal(err)
	}
	return exporttest.ZipDirAs(t, claudeExport, map[string][]byte{"conversations.json": out})
}

func laterExport(convs []any) []any {
	for _, c := range convs {
		m, ok := c.(map[string]any)
		if !ok || m["uuid"] != "c0000000-0000-4000-8000-000000000002" {
			continue
		}
		msgs := m["chat_messages"].([]any)
		last, _ := json.Marshal(msgs[len(msgs)-1])
		var more map[string]any
		_ = json.Unmarshal(last, &more)
		more["uuid"] = "m0000000-0000-4000-8000-0000000000ff"
		more["text"] = "CONTINUED later on"
		more["content"] = []any{map[string]any{"type": "text", "text": "CONTINUED later on"}}
		more["created_at"] = "2026-01-06T10:00:00.000000Z"
		m["chat_messages"] = append(msgs, more)
		m["updated_at"] = "2026-01-06T10:00:00.000000Z"
	}
	return append(convs, map[string]any{
		"uuid": "c0000000-0000-4000-8000-000000000009", "name": "BRANDNEW conversation",
		"created_at": "2026-01-06T11:00:00.000000Z", "updated_at": "2026-01-06T11:05:00.000000Z",
		"chat_messages": []any{map[string]any{"uuid": "m0000000-0000-4000-8000-0000000000aa", "sender": "human", "text": "a brand new question",
			"content": []any{map[string]any{"type": "text", "text": "a brand new question"}}, "created_at": "2026-01-06T11:00:00.000000Z"}},
	})
}

// Importing the same export twice adds nothing; a newer export adds only its
// new conversations.
func TestReimportingExportsIsIncremental(t *testing.T) {
	m := newMulti(t)
	first := exporttest.ZipDir(t, claudeExport)
	if err := m.run("import-claude-ai", first); err != nil {
		t.Fatal(err)
	}
	if out := m.out.String(); !strings.Contains(out, "Registered claude.ai export: 3 conversation(s), 2 skipped (unknown shape).") || strings.Contains(out, "already imported") {
		t.Fatalf("first import: %s", out)
	}
	// the same file again
	if err := m.run("import-claude-ai", first); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.out.String(), "already registered") {
		t.Fatalf("same file twice: %s", m.out)
	}
	// the same export saved under another name: nothing new, not registered
	if err := m.run("import-claude-ai", exporttest.ZipDir(t, claudeExport)); err != nil {
		t.Fatal(err)
	}
	if out := m.out.String(); !strings.Contains(out, "Nothing new in this claude.ai export: all 3 conversation(s) are already imported") || !strings.Contains(out, "not registered") {
		t.Fatalf("same content, other file: %s", out)
	}
	_ = m.run("imports", "list")
	if out := m.out.String(); strings.Count(out, "claude-ai") != 1 || !strings.Contains(out, "3 new, 0 changed, of 3") {
		t.Fatalf("one export registered, with what it brought in: %s", out)
	}
	_ = m.run("chats", "--all", "--limit", "0")
	if len(listed(m.out.String())) != 3 {
		t.Fatalf("three conversations, once each: %s", m.out)
	}

	// share the ungrouped chats as a whole and send them, so a change can be seen
	if err := m.run("share", "add", "claude-ai:Chats"); err != nil {
		t.Fatal(err)
	}
	_ = m.run("sync", "--dry-run", "--approve")
	before := len(chatPayloads(m))

	// a newer export: one conversation continued, one new
	if err := m.run("import-claude-ai", newerClaudeExport(t, laterExport)); err != nil {
		t.Fatalf("%v\n%s", err, m.errOut)
	}
	out := m.out.String()
	for _, want := range []string{
		"Registered claude.ai export: 4 conversation(s), 1 new, 1 changed, 2 already imported, 2 skipped (unknown shape).",
		"claude-ai:Chats", "1 new conversation(s) are in groupings you share",
		"1 conversation(s) continued since the last export",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("newer import lacks %q:\n%s", want, out)
		}
	}
	_ = m.run("imports", "list")
	if out := m.out.String(); strings.Count(out, "claude-ai") != 2 || !strings.Contains(out, "1 new, 1 changed, of 4") {
		t.Fatalf("imports shows what each export added: %s", out)
	}
	// every conversation is still listed once, and the continued one carries its new text
	_ = m.run("chats", "--all", "--limit", "0")
	if got := listed(m.out.String()); len(got) != 4 {
		t.Fatalf("four conversations, once each: %v", got)
	}
	_ = m.run("sync", "--dry-run", "--approve")
	after := chatPayloads(m)
	all := m.out.String()
	if len(after) != before+1 || !strings.Contains(all, "BRANDNEW") || !strings.Contains(all, "CONTINUED") {
		t.Fatalf("the new conversation joins and the continued one is current: %d -> %d", before, len(after))
	}

	// an older export after a newer one changes nothing
	if err := m.run("import-claude-ai", exporttest.ZipDir(t, claudeExport)); err != nil {
		t.Fatal(err)
	}
	if out := m.out.String(); !strings.Contains(out, "Nothing new") || !strings.Contains(out, "1 of them in a more recent version") {
		t.Fatalf("older export: %s", out)
	}
}

// Removing an export forgets the file and deletes nothing that was sent.
func TestRemovingAnExportDeletesNothingSent(t *testing.T) {
	m := newMulti(t)
	if err := m.run("import-claude-ai", exporttest.ZipDir(t, claudeExport)); err != nil {
		t.Fatal(err)
	}
	if err := m.run("share", "add", "claude-ai:Chats"); err != nil {
		t.Fatal(err)
	}
	_ = m.run("imports", "list")
	id := strings.Fields(strings.Split(strings.TrimSpace(m.out.String()), "\n")[1])[0]
	if err := m.run("imports", "remove", id); err != nil {
		t.Fatal(err)
	}
	if out := m.out.String(); !strings.Contains(out, "Nothing was deleted") || !strings.Contains(out, "sessions retract") {
		t.Fatalf("remove says what it does not do: %s", out)
	}
	// the conversations are gone from the listing, the sharing rule is untouched
	_ = m.run("chats", "--all")
	if !strings.Contains(m.out.String(), "no imported conversations") {
		t.Fatalf("after remove: %s", m.out)
	}
	_ = m.run("share", "list")
	if !strings.Contains(m.out.String(), "claude-ai:Chats") {
		t.Fatalf("the share rule stays: %s", m.out)
	}
}
