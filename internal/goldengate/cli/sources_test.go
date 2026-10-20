package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/claudecode"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/codex"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/export/exporttest"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/opencode"
	"github.com/divyanshusingh2903/goldengate/internal/model"
)

// multi sets up a laptop that has all three coding tools plus chat exports.
type multi struct {
	t           *testing.T
	out, errOut *bytes.Buffer
	deps        Deps
	root        string
}

func newMulti(t *testing.T) *multi {
	t.Helper()
	root := t.TempDir()
	m := &multi{t: t, root: root}
	// Claude Code
	claudeDir := filepath.Join(root, "claude")
	copyFile(t, "../../../testdata/claude-code/2.1/workitems.jsonl", filepath.Join(claudeDir, "projects", "-p", "11111111-1111-4111-8111-111111111111.jsonl"))
	// Codex
	codexHome := filepath.Join(root, "codex")
	copyFile(t, "../../../testdata/codex/0.15x/workitems.jsonl", filepath.Join(codexHome, "sessions", "2026", "01", "05", "rollout-2026-01-05T09-00-00-00000000-0000-4000-8000-0000000000bb.jsonl"))
	// OpenCode
	ocDir := filepath.Join(root, "opencode")
	_ = os.MkdirAll(ocDir, 0o755)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(ocDir, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"schema.sql", "basic.sql"} {
		b, _ := os.ReadFile(filepath.Join("../../../testdata/opencode/1.18", f))
		if _, err := db.ExecContext(context.Background(), string(b)); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	m.out, m.errOut = &bytes.Buffer{}, &bytes.Buffer{}
	env := map[string]string{"XDG_CONFIG_HOME": filepath.Join(root, "cfg"), "XDG_STATE_HOME": filepath.Join(root, "state")}
	m.deps = Deps{
		Stdout: m.out, Stderr: m.errOut, Home: "/home/user", Getenv: func(k string) string { return env[k] },
		NewRegistry: func(string, func(string) string) (*adapters.Registry, error) {
			var r adapters.Registry
			for _, a := range []adapters.Adapter{claudecode.New(claudecode.Options{ConfigDir: claudeDir}), codex.New(codex.Options{CodexHome: codexHome}), opencode.New(opencode.Options{DataDir: ocDir})} {
				if err := r.Register(a); err != nil {
					return nil, err
				}
			}
			return &r, nil
		},
		Now: func() time.Time { return time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC) },
	}
	return m
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (m *multi) run(args ...string) error {
	m.out.Reset()
	m.errOut.Reset()
	cmd := New(m.deps)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(context.Background())
}

func (m *multi) payloads() []model.SessionPayload {
	m.t.Helper()
	var out []model.SessionPayload
	dec := json.NewDecoder(strings.NewReader(m.out.String()))
	for dec.More() {
		var p model.SessionPayload
		if err := dec.Decode(&p); err != nil {
			m.t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func TestOneFolderAcrossThreeToolsIsOneProject(t *testing.T) {
	m := newMulti(t)
	if err := m.run("share", "list", "--projects"); err != nil {
		t.Fatalf("%v\n%s", err, m.errOut)
	}
	listing := m.out.String()
	if strings.Count(listing, "~/projects/demo ") != 1 {
		t.Fatalf("the same folder in three tools must be one project:\n%s", listing)
	}
	if err := m.run("share", "add", "~/projects/demo"); err != nil {
		t.Fatal(err)
	}
	if err := m.run("sync", "--dry-run"); err != nil {
		t.Fatalf("%v\n%s", err, m.errOut)
	}
	sources := map[string]bool{}
	for _, p := range m.payloads() {
		sources[p.Source] = true
		if p.Project != "~/projects/demo" {
			t.Errorf("%s filed under %q", p.Source, p.Project)
		}
	}
	for _, want := range []string{"claude-code", "codex", "opencode"} {
		if !sources[want] {
			t.Errorf("no payload from %s: %v", want, sources)
		}
	}
	// secrets that live next to the sessions never appear
	all := m.out.String() + m.errOut.String()
	for _, bad := range []string{"TOPSECRET", "hunter2token", "SECRET-ISSUE-BODY", "INJECTED CONTEXT", "SYSTEM INSTRUCTIONS"} {
		if strings.Contains(all, bad) {
			t.Errorf("%q leaked into the preview", bad)
		}
	}
	// the OpenCode sub-agent session and unselected folders add nothing
	if len(m.payloads()) != 3+2 { // claude 1, codex 1, opencode: main + old (+ worktree/other are different folders)
		t.Logf("payloads: %d", len(m.payloads()))
	}
}

func TestSourceFilterPicksOneTool(t *testing.T) {
	m := newMulti(t)
	_ = m.run("share", "add", "~/projects/demo")
	if err := m.run("sync", "--dry-run", "--source", "codex"); err != nil {
		t.Fatal(err)
	}
	for _, p := range m.payloads() {
		if p.Source != "codex" {
			t.Errorf("--source codex returned %s", p.Source)
		}
	}
	if len(m.payloads()) == 0 {
		t.Error("expected codex payloads")
	}
}

func TestChatExportsNeedAnExplicitAllow(t *testing.T) {
	m := newMulti(t)
	claude := exporttest.ZipDir(t, "../../../testdata/claude-ai/export")
	gpt := exporttest.ZipDir(t, "../../../testdata/chatgpt/export")

	if err := m.run("import-claude-ai", claude); err != nil {
		t.Fatalf("%v\n%s", err, m.errOut)
	}
	if out := m.out.String(); !strings.Contains(out, "3 conversation(s)") || !strings.Contains(out, "claude-ai:Demo Project") || !strings.Contains(out, "Nothing is shared yet") {
		t.Fatalf("import summary:\n%s", out)
	}
	if err := m.run("import-chatgpt", gpt); err != nil {
		t.Fatal(err)
	}
	// default-deny, and even default=allow does not cover chat history
	_ = m.run("sync", "--dry-run")
	for _, p := range m.payloads() {
		if strings.Contains(p.Source, "claude-ai") || p.Source == "chatgpt" {
			t.Fatalf("chat history shared without an explicit allow: %s", p.Project)
		}
	}
	cfgPath := filepath.Join(m.root, "cfg", "goldengate", "config.toml")
	b, _ := os.ReadFile(cfgPath)
	_ = os.WriteFile(cfgPath, []byte(strings.Replace(string(b), `default = 'deny'`, `default = 'allow'`, 1)), 0o600)
	_ = m.run("sync", "--dry-run")
	for _, p := range m.payloads() {
		if p.Source == "claude-ai" || p.Source == "chatgpt" {
			t.Fatal("default = allow must not share chat history")
		}
	}
	_ = os.WriteFile(cfgPath, b, 0o600)

	// one explicit project
	if err := m.run("share", "add", "claude-ai:Demo Project"); err != nil {
		t.Fatal(err)
	}
	if err := m.run("sync", "--dry-run"); err != nil {
		t.Fatalf("%v\n%s", err, m.errOut)
	}
	var chat []model.SessionPayload
	for _, p := range m.payloads() {
		if p.Source == "claude-ai" || p.Source == "chatgpt" {
			chat = append(chat, p)
		}
	}
	if len(chat) != 1 || chat[0].Project != "claude-ai:Demo Project" || chat[0].Summary == nil {
		t.Fatalf("only the allowed bucket may be shared: %+v", chat)
	}
	all := m.out.String()
	for _, bad := range []string{"SECRET", "PRIVATE", "Recipe", "pasta", "someone@example.invalid", "Side"} {
		if strings.Contains(all, bad) {
			t.Errorf("%q leaked from a chat export", bad)
		}
	}
	var arts int
	for _, w := range chat[0].WorkItems {
		if w.Kind == model.KindArtifact {
			arts++
		}
	}
	if arts != 3 {
		t.Errorf("artifacts and uploads travel as metadata: %d", arts)
	}
	// a chatgpt project glob
	if err := m.run("share", "add", "chatgpt:*"); err != nil {
		t.Fatal(err)
	}
	_ = m.run("sync", "--dry-run")
	n := 0
	for _, p := range m.payloads() {
		if p.Source == "chatgpt" {
			n++
		}
	}
	if n != 4 {
		t.Errorf("chatgpt:* should cover its 4 conversations, got %d", n)
	}
	// --project filter works for chat keys
	_ = m.run("sync", "--dry-run", "--project", "chatgpt:Chats")
	for _, p := range m.payloads() {
		if p.Project != "chatgpt:Chats" {
			t.Errorf("--project chatgpt:Chats returned %s", p.Project)
		}
	}
}

func TestImportsListRemoveAndRefusals(t *testing.T) {
	m := newMulti(t)
	good := exporttest.ZipDir(t, "../../../testdata/claude-ai/export")
	if err := m.run("import-claude-ai", good); err != nil {
		t.Fatal(err)
	}
	if err := m.run("import-claude-ai", good); err != nil { // idempotent
		t.Fatal(err)
	}
	_ = m.run("imports", "list")
	if strings.Count(m.out.String(), "claude-ai") != 1 {
		t.Fatalf("one archive, one row:\n%s", m.out)
	}
	// the wrong product, junk and missing files are refused with a clear error
	gpt := exporttest.ZipDir(t, "../../../testdata/chatgpt/export")
	if err := m.run("import-claude-ai", gpt); err == nil {
		t.Error("a ChatGPT export was accepted as claude.ai")
	}
	junk := filepath.Join(t.TempDir(), "x.zip")
	_ = os.WriteFile(junk, []byte("nope"), 0o600)
	if err := m.run("import-chatgpt", junk); err == nil || !strings.Contains(err.Error(), "does not look like") {
		t.Errorf("junk: %v", err)
	}
	if err := m.run("import-chatgpt", filepath.Join(t.TempDir(), "gone.zip")); err == nil {
		t.Error("missing file")
	}
	if err := m.run("imports", "remove", "abc"); err == nil {
		t.Error("bad id")
	}
	if err := m.run("imports", "remove", "1"); err != nil {
		t.Fatal(err)
	}
	if err := m.run("imports", "remove", "1"); err == nil {
		t.Error("removing twice")
	}
	_ = m.run("share", "add", "claude-ai:Chats")
	_ = m.run("sync", "--dry-run")
	if len(m.payloads()) != 0 {
		t.Error("a removed import must stop being read")
	}
}

func TestUnsharingChatProject(t *testing.T) {
	m := newMulti(t)
	_ = m.run("import-claude-ai", exporttest.ZipDir(t, "../../../testdata/claude-ai/export"))
	_ = m.run("share", "add", "claude-ai:Chats")
	if err := m.run("share", "remove", "claude-ai:Chats"); err != nil {
		t.Fatalf("%v\n%s", err, m.errOut)
	}
	if !strings.Contains(m.out.String(), "1 project(s) un-shared") {
		t.Errorf("un-sharing a chat project:\n%s", m.out)
	}
}
