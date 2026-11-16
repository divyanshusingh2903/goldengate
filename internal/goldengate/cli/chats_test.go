package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/export/exporttest"
	"github.com/divyanshusingh2903/goldengate/internal/model"
)

// chatPayloads are the payloads of the last run that come from chat exports.
func chatPayloads(m *multi) []model.SessionPayload {
	var out []model.SessionPayload
	for _, p := range m.payloads() {
		if p.Source == "claude-ai" || p.Source == "chatgpt" {
			out = append(out, p)
		}
	}
	return out
}

var reChatLine = regexp.MustCompile(`(?m)^  (\S+)  (claude-ai|chatgpt)\s+\S+  (.*?)  \[(.*?); from (\S.*?)\]$`)

// listed parses `goldengate chats` output into id -> grouping.
func listed(out string) map[string]string {
	got := map[string]string{}
	for _, m := range reChatLine.FindAllStringSubmatch(out, -1) {
		got[m[1]] = m[5]
	}
	return got
}

// Sharing one conversation sends that conversation only.
func TestSharingOneConversationSendsOnlyThatConversation(t *testing.T) {
	m := newMulti(t)
	if err := m.run("import-claude-ai", exporttest.ZipDir(t, "../../../testdata/claude-ai/export")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.out.String(), "goldengate chats") {
		t.Fatalf("the import points at single conversations: %s", m.out)
	}
	if err := m.run("import-chatgpt", exporttest.ZipDir(t, "../../../testdata/chatgpt/export")); err != nil {
		t.Fatal(err)
	}

	// listing shows every unassigned conversation and sends nothing
	if err := m.run("chats", "--limit", "0"); err != nil {
		t.Fatal(err)
	}
	all := listed(m.out.String())
	if len(all) != 7 || !strings.Contains(m.out.String(), "nothing here is sent by listing") {
		t.Fatalf("7 imported conversations, all in no project:\n%s", m.out)
	}
	var loose []string // conversations of the catch-all groupings
	for id, group := range all {
		if group == "claude-ai:Chats" || group == "chatgpt:Chats" {
			loose = append(loose, id)
		}
	}
	if len(loose) < 3 {
		t.Fatalf("the fixtures have ungrouped chats in both products: %v", all)
	}
	var one, sibling string
	for _, id := range loose {
		switch {
		case all[id] == "chatgpt:Chats" && one == "":
			one = id
		case all[id] == "chatgpt:Chats":
			sibling = id
		}
	}
	if one == "" || sibling == "" {
		t.Fatalf("need two ungrouped ChatGPT chats: %v", all)
	}

	// share exactly one conversation: it waits; approving sends it and nothing else
	if err := m.run("chats", "share", one, "--project", "Research"); err != nil {
		t.Fatalf("%v\n%s", err, m.errOut)
	}
	if out := m.out.String(); !strings.Contains(out, `created project "Research"`) || !strings.Contains(out, "Only these conversations are shared") {
		t.Fatalf("share: %s", out)
	}
	_ = m.run("sync", "--dry-run")
	if got := chatPayloads(m); len(got) != 0 {
		t.Fatalf("a shared conversation still waits for approval: %d payloads", len(got))
	}
	_ = m.run("sync", "--dry-run", "--approve")
	got := chatPayloads(m)
	if len(got) != 1 || got[0].SessionID != one || got[0].Project != "chatgpt:Chats" || got[0].ProjectName != "Research" {
		t.Fatalf("exactly the shared conversation is sent: %+v", got)
	}
	// the listing says where each conversation stands
	_ = m.run("chats", "--all", "--limit", "0")
	if out := m.out.String(); !strings.Contains(out, `[in "Research"; from chatgpt:Chats]`) || strings.Count(out, "in no project") != 6 {
		t.Fatalf("chats --all: %s", out)
	}
	_ = m.run("inbox")
	if !strings.Contains(m.out.String(), "6 conversation(s) from your chat exports are in no project") {
		t.Fatalf("the inbox counts unassigned chats: %s", m.out)
	}

	// a second conversation of the same grouping goes to another project
	if err := m.run("chats", "share", sibling, "--project", "Travel"); err != nil {
		t.Fatal(err)
	}
	_ = m.run("sync", "--dry-run", "--approve")
	byID := map[string]string{}
	for _, p := range chatPayloads(m) {
		byID[p.SessionID] = p.ProjectName
	}
	if len(byID) != 2 || byID[one] != "Research" || byID[sibling] != "Travel" {
		t.Fatalf("one grouping, two projects, two conversations: %v", byID)
	}
	// move
	if err := m.run("chats", "move", sibling, "Research"); err != nil || !strings.Contains(m.out.String(), `from "Travel"`) {
		t.Fatalf("move: %v %s", err, m.out)
	}
	_ = m.run("sync", "--dry-run", "--approve")
	for _, p := range chatPayloads(m) {
		if p.ProjectName != "Research" {
			t.Fatalf("after the move both are in Research: %+v", p.ProjectName)
		}
	}
	cfg, _ := os.ReadFile(filepath.Join(m.root, "cfg", "goldengate", "config.toml"))
	if !strings.Contains(string(cfg), "sessions = [") || strings.Count(string(cfg), "path = 'chatgpt:Chats'") != 1 {
		t.Fatalf("the config lists the conversations under one member:\n%s", cfg)
	}

	// unshare: back to nothing
	if err := m.run("chats", "unshare", one, sibling); err != nil {
		t.Fatal(err)
	}
	_ = m.run("sync", "--dry-run", "--approve")
	if got := chatPayloads(m); len(got) != 0 {
		t.Fatalf("unshared conversations are not sent: %+v", got)
	}

	for _, bad := range [][]string{
		{"chats", "share", one},                            // no project
		{"chats", "share", "no-such-id", "--project", "X"}, // unknown id
		{"chats", "unshare", "no-such-id"},
	} {
		if err := m.run(bad...); err == nil {
			t.Errorf("%v must be refused", bad)
		}
	}
}

// A grouping shared as a whole keeps working exactly as before, and cannot
// be mixed with sharing by conversation.
func TestWholeGroupingSharesStillWorkAndDoNotMix(t *testing.T) {
	m := newMulti(t)
	if err := m.run("import-claude-ai", exporttest.ZipDir(t, "../../../testdata/claude-ai/export")); err != nil {
		t.Fatal(err)
	}
	if err := m.run("share", "add", "claude-ai:Chats", "--auto-approve"); err != nil {
		t.Fatal(err)
	}
	_ = m.run("sync", "--dry-run")
	whole := chatPayloads(m)
	if len(whole) == 0 {
		t.Fatal("a whole grouping shared as before sends its conversations")
	}
	for _, p := range whole {
		if p.Project != "claude-ai:Chats" {
			t.Fatalf("only that grouping: %+v", p.Project)
		}
	}
	// single conversations cannot be added next to the whole grouping
	if err := m.run("chats", "share", whole[0].SessionID, "--project", "Other"); err == nil || !strings.Contains(err.Error(), "already shared") {
		t.Fatalf("mixing is refused: %v", err)
	}
	if err := m.run("chats", "unshare", whole[0].SessionID); err == nil || !strings.Contains(err.Error(), "sessions exclude") {
		t.Fatalf("unsharing one of a whole grouping points at exclude: %v", err)
	}
	// the other direction: once shared by conversation, `share add` does not silently widen it
	if err := m.run("share", "remove", "claude-ai:Chats", "--keep-remote"); err != nil {
		t.Fatal(err)
	}
	if err := m.run("chats", "share", whole[0].SessionID, "--project", "Other"); err != nil {
		t.Fatal(err)
	}
	if err := m.run("share", "add", "claude-ai:Chats"); err == nil || !strings.Contains(err.Error(), "single conversations") {
		t.Fatalf("share add on a grouping shared by conversation: %v", err)
	}
	// ... but saying so explicitly replaces the list with the whole grouping
	if err := m.run("projects", "add", "Other", "claude-ai:Chats"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(m.root, "cfg", "goldengate", "config.toml"))
	if strings.Contains(string(cfg), "sessions = [") {
		t.Fatalf("the whole grouping replaced the list:\n%s", cfg)
	}
}

// A project made in claude.ai is suggested as a named project.
func TestChatProjectsAreSuggestedAsNamedProjects(t *testing.T) {
	m := newMulti(t)
	if err := m.run("import-claude-ai", exporttest.ZipDir(t, "../../../testdata/claude-ai/export")); err != nil {
		t.Fatal(err)
	}
	if err := m.run("projects", "suggest"); err != nil {
		t.Fatal(err)
	}
	out := m.out.String()
	id := regexp.MustCompile(`suggestion (s_[0-9a-f]{8}): Make "Demo Project" a project\?`).FindStringSubmatch(out)
	if id == nil || !strings.Contains(out, "It is a project in claude.ai with 1 conversation(s)") || strings.Contains(out, `"Chats"`) ||
		!strings.Contains(out, "nothing is sent by accepting") {
		t.Fatalf("the claude.ai project is suggested, the catch-all is not:\n%s", out)
	}
	_ = m.run("sync", "--dry-run", "--approve")
	if len(chatPayloads(m)) != 0 {
		t.Fatal("a suggestion shares nothing")
	}
	if err := m.run("projects", "accept", id[1]); err != nil {
		t.Fatal(err)
	}
	_ = m.run("sync", "--dry-run", "--approve")
	got := chatPayloads(m)
	if len(got) != 1 || got[0].Project != "claude-ai:Demo Project" || got[0].ProjectName != "Demo Project" {
		t.Fatalf("accepting makes the project and shares its conversations: %+v", got)
	}
	_ = m.run("projects", "suggest")
	if strings.Contains(m.out.String(), "Demo Project") {
		t.Fatalf("an accepted suggestion is not made again: %s", m.out)
	}
}
