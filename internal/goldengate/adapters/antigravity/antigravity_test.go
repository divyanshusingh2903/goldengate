package antigravity_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	_ "modernc.org/sqlite"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/antigravity"
	"github.com/divyanshusingh2903/goldengate/internal/model"
)

// Everything here is made up: no real session is in the repository.

const (
	idMain  = "11111111-1111-4111-8111-111111111111"
	idCLI   = "22222222-2222-4222-8222-222222222222"
	idSub   = "33333333-3333-4333-8333-333333333333"
	idNoDir = "44444444-4444-4444-8444-444444444444"
	idOld   = "55555555-5555-4555-8555-555555555555"
)

type row struct {
	id, title, workspaces, appDir, parent string
	depth                                 int
}

func index(t *testing.T, dir string, rows ...row) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE conversation_summaries (conversation_id text PRIMARY KEY, title text NOT NULL DEFAULT '',
		last_modified_time datetime NOT NULL DEFAULT '', workspace_uris text NOT NULL DEFAULT '', parent_conversation_id text NOT NULL DEFAULT '',
		nesting_depth integer NOT NULL DEFAULT 0, app_data_dir text NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := db.ExecContext(ctx, `INSERT INTO conversation_summaries(conversation_id, title, workspace_uris, app_data_dir, parent_conversation_id, nesting_depth)
			VALUES(?,?,?,?,?,?)`, r.id, r.title, r.workspaces, r.appDir, r.parent, r.depth); err != nil {
			t.Fatal(err)
		}
	}
}

func transcript(t *testing.T, dir, id string, steps ...map[string]any) string {
	t.Helper()
	p := filepath.Join(dir, "brain", id, ".system_generated", "logs", "transcript_full.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, s := range steps {
		line, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// metadata writes conversations/<id>.db with a trajectory_metadata_blob in
// the shape seen on disk.
func metadata(t *testing.T, dir, id, remote, branch string) {
	t.Helper()
	var repo, ws, blob []byte
	repo = protowire.AppendTag(repo, 1, protowire.BytesType)
	repo = protowire.AppendString(repo, "owner/repo")
	repo = protowire.AppendTag(repo, 2, protowire.BytesType)
	repo = protowire.AppendString(repo, remote)
	ws = protowire.AppendTag(ws, 1, protowire.BytesType)
	ws = protowire.AppendString(ws, "file:///home/u/code/app")
	ws = protowire.AppendTag(ws, 3, protowire.BytesType)
	ws = protowire.AppendBytes(ws, repo)
	ws = protowire.AppendTag(ws, 4, protowire.BytesType)
	ws = protowire.AppendString(ws, branch)
	blob = protowire.AppendTag(blob, 1, protowire.BytesType)
	blob = protowire.AppendBytes(blob, ws)
	blob = protowire.AppendTag(blob, 6, protowire.BytesType)
	blob = protowire.AppendString(blob, id)
	if err := os.MkdirAll(filepath.Join(dir, "conversations"), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "conversations", id+".db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "CREATE TABLE trajectory_metadata_blob (id text DEFAULT 'main', data blob, PRIMARY KEY (id))"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO trajectory_metadata_blob(id, data) VALUES('main', ?)", blob); err != nil {
		t.Fatal(err)
	}
}

func user(at, text string) map[string]any {
	return map[string]any{"type": "USER_INPUT", "source": "USER_EXPLICIT", "status": "DONE", "created_at": at,
		"content": "<USER_REQUEST>\n" + text + "\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: now.\n</ADDITIONAL_METADATA>\n<USER_SETTINGS_CHANGE>\nmodel changed\n</USER_SETTINGS_CHANGE>"}
}

type call struct {
	name string
	args map[string]any
}

func planner(at, text string, calls ...call) map[string]any {
	m := map[string]any{"type": "PLANNER_RESPONSE", "source": "MODEL", "status": "DONE", "created_at": at, "thinking": "SECRET-THINKING"}
	if text != "" {
		m["content"] = text
	}
	var tc []map[string]any
	for _, c := range calls {
		tc = append(tc, map[string]any{"name": c.name, "args": c.args})
	}
	if tc != nil {
		m["tool_calls"] = tc
	}
	return m
}

func result(at, status, body string) map[string]any {
	return map[string]any{"type": "GENERIC", "source": "MODEL", "status": status, "created_at": at,
		"content": "Created At: " + at + "\nCompleted At: " + at + "\n" + body}
}

func other(typ, at string) map[string]any {
	return map[string]any{"type": typ, "source": "SYSTEM", "status": "DONE", "created_at": at, "content": "CHECKPOINT-SUMMARY harness text"}
}

func list(t *testing.T, a *antigravity.Adapter, opt adapters.ListOpts) ([]model.SessionRef, []error) {
	t.Helper()
	var refs []model.SessionRef
	var errs []error
	for r, err := range a.ListSessions(context.Background(), opt) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		refs = append(refs, r)
	}
	return refs, errs
}

// The two stores as found on disk: agy's conversations are also in the IDE's
// index, marked as the IDE's, but their files are in agy's directory.
func setup(t *testing.T) (*antigravity.Adapter, string, string) {
	t.Helper()
	root := t.TempDir()
	ide, cli := filepath.Join(root, "antigravity"), filepath.Join(root, "antigravity-cli")
	index(t, ide,
		row{id: idMain, title: "Build the widget", workspaces: `["file:///home/u/code/my%20app"]`, appDir: "antigravity"},
		row{id: idCLI, title: "From agy", workspaces: `["file:///home/u/code/cli"]`, appDir: "antigravity"},
		row{id: idNoDir, title: "Loose", workspaces: `["vscode-remote://ssh/home/u"]`, appDir: "antigravity"},
		row{id: idOld, title: "Old", workspaces: `["file:///home/u/code/old"]`, appDir: "antigravity"},
		row{id: "../escape", title: "Bad", appDir: "antigravity"},
	)
	index(t, cli,
		row{id: idCLI, title: "From agy", workspaces: `["file:///home/u/code/cli"]`, appDir: "antigravity-cli"},
		row{id: idSub, title: "Sub", workspaces: `["file:///home/u/code/cli"]`, appDir: "antigravity-cli", parent: idCLI, depth: 1},
	)
	transcript(t, ide, idMain, user("2026-11-21T10:00:00Z", "build it"))
	transcript(t, ide, idNoDir, user("2026-11-21T10:00:00Z", "hello"))
	transcript(t, cli, idCLI, user("2026-11-21T11:00:00Z", "from the cli"))
	transcript(t, cli, idSub, user("2026-11-21T11:00:00Z", "sub"))
	for _, d := range []string{ide, cli} {
		if err := os.MkdirAll(filepath.Join(d, "conversations"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{filepath.Join(ide, "conversations", idOld+".pb"), filepath.Join(cli, "conversations", "66666666-6666-4666-8666-666666666666.pb")} {
		if err := os.WriteFile(p, []byte{0xa0, 0xef, 0x9c}, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return antigravity.New(antigravity.Options{Dirs: []string{ide, cli}}), ide, cli
}

func TestDetect(t *testing.T) {
	a, ide, _ := setup(t)
	d, err := a.Detect(context.Background())
	if err != nil || !d.Installed || d.Root != ide {
		t.Fatalf("detect: %+v %v", d, err)
	}
	if !slices.ContainsFunc(d.Notes, func(n string) bool { return strings.Contains(n, "2 older conversation(s)") }) {
		t.Errorf("the encrypted conversations of both stores are counted: %v", d.Notes)
	}
	none := antigravity.New(antigravity.Options{Dirs: []string{filepath.Join(t.TempDir(), "nothing")}})
	if d, err := none.Detect(context.Background()); err != nil || d.Installed || len(d.Notes) == 0 {
		t.Fatalf("absent: %+v %v", d, err)
	}
}

func TestListSessions(t *testing.T) {
	a, ide, cli := setup(t)
	refs, errs := list(t, a, adapters.ListOpts{})
	got := map[string]model.SessionRef{}
	for _, r := range refs {
		got[r.ID] = r
	}
	if len(refs) != 3 {
		t.Fatalf("listed %d: %+v", len(refs), refs)
	}
	if r := got[idMain]; r.ProjectKey != "/home/u/code/my app" || r.Path != filepath.Join(ide, "brain", idMain, ".system_generated", "logs", "transcript_full.jsonl") ||
		r.SizeBytes == 0 || r.UpdatedAt.IsZero() || !r.StartedAt.IsZero() {
		t.Errorf("main: %+v", r)
	}
	// listed from agy's directory, although the IDE's index names it first
	if r := got[idCLI]; !strings.HasPrefix(r.Path, cli) || r.ProjectKey != "/home/u/code/cli" {
		t.Errorf("agy conversation: %+v", r)
	}
	if r := got[idNoDir]; r.ProjectKey != antigravity.NoFolder {
		t.Errorf("a remote workspace has no local folder: %+v", r)
	}
	if _, ok := got[idSub]; ok {
		t.Error("a subagent conversation was listed")
	}
	if _, ok := got[idOld]; ok {
		t.Error("a conversation without a transcript was listed")
	}
	if len(errs) != 1 || !adapters.IsSkip(errs[0]) || !strings.Contains(errs[0].Error(), "unusable id") {
		t.Errorf("the bad id is a skip warning: %v", errs)
	}

	// Since compares the transcript's modification time
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(got[idNoDir].Path, old, old); err != nil {
		t.Fatal(err)
	}
	refs, _ = list(t, a, adapters.ListOpts{Since: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)})
	if len(refs) != 2 || slices.ContainsFunc(refs, func(r model.SessionRef) bool { return r.ID == idNoDir }) {
		t.Errorf("since: %+v", refs)
	}
}

// Listing never opens a transcript: the daemon lists every two minutes, and
// conversations of folders nobody shares must not be read.
func TestListSessionsDoesNotReadTranscripts(t *testing.T) {
	a, ide, _ := setup(t)
	p := filepath.Join(ide, "brain", idMain, ".system_generated", "logs", "transcript_full.jsonl")
	if err := os.WriteFile(p, []byte("not json at all\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(p, 0o600) }()
	refs, errs := list(t, a, adapters.ListOpts{})
	if len(refs) != 3 || len(errs) != 1 {
		t.Fatalf("an unreadable transcript is still listed: %d %v", len(refs), errs)
	}
}

func TestLoad(t *testing.T) {
	a, ide, _ := setup(t)
	big := strings.Repeat("x", 100<<10) // a first line well over bufio's default 64 KB
	transcript(t, ide, idMain,
		user("2026-11-21T10:00:00Z", "build the widget "+big),
		planner("2026-11-21T10:00:05Z", "Looking first.",
			call{"run_command", map[string]any{"CommandLine": "git commit -m widget", "WaitMsBeforeAsync": 1000}},
			call{"read_file", map[string]any{"Path": "/home/u/code/my app/main.go"}},
			call{"edit_file", map[string]any{"Path": "/home/u/code/my app/main.go", "Target": "a", "Replacement": "b"}},
			call{"write_file", map[string]any{"Path": "/home/u/code/my app/new.go", "Content": "package x"}},
			call{"edit_file", map[string]any{"Path": "/home/u/code/my app/broken.go"}},
			call{"run_command", map[string]any{"CommandLine": "go test ./..."}},
		),
		result("2026-11-21T10:00:06Z", "DONE", "Command exited with code 0.\n\nCommand output:\n[main abc1234] widget"),
		result("2026-11-21T10:00:07Z", "DONE", "Showing lines 1-10 of 10"),
		result("2026-11-21T10:00:08Z", "DONE", "Made 1 replacement in /home/u/code/my app/main.go"),
		result("2026-11-21T10:00:09Z", "DONE", "Created file."),
		result("2026-11-21T10:00:10Z", "ERROR", "Encountered error in step execution: target not found"),
		// the second command never got a result: the turn ends here
		other("CHECKPOINT", "2026-11-21T10:00:11Z"),
		planner("2026-11-21T10:00:12Z", "",
			call{"run_command", map[string]any{"CommandLine": "npm run dev", "IsDaemon": true}},
			call{"edit_file", map[string]any{"Path": "/home/u/code/my app/main.go"}},
			call{"run_command", map[string]any{"CommandLine": "go vet ./..."}},
		),
		result("2026-11-21T10:00:13Z", "RUNNING", "Tool is running as a background task."),
		result("2026-11-21T10:00:14Z", "INVALID", "Error invalid tool call: invalid arguments"),
		result("2026-11-21T10:00:15Z", "DONE", "Command exited with code 2.\n\nCommand output:\nvet: problems"),
		planner("2026-11-21T10:00:16Z", "Done: the widget is committed."),
		other("SYSTEM_MESSAGE", "2026-11-21T10:00:17Z"),
		other("ERROR_MESSAGE", "2026-11-21T10:00:18Z"),
		map[string]any{"type": "SOMETHING_NEW", "status": "DONE", "created_at": "2026-11-21T10:00:19Z"},
		map[string]any{"type": "USER_INPUT", "status": "DONE", "created_at": "2026-11-21T10:01:00Z",
			"content": "<USER_REQUEST>\nunclosed request\n<ADDITIONAL_METADATA>\ntime\n</ADDITIONAL_METADATA>"},
	)
	metadata(t, ide, idMain, "https://user:token@github.com/owner/repo.git", "feat/widget")
	refs, _ := list(t, a, adapters.ListOpts{})
	var ref model.SessionRef
	for _, r := range refs {
		if r.ID == idMain {
			ref = r
		}
	}
	s, err := a.Load(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Build the widget" || s.GitBranch != "feat/widget" || s.GitRemote != "https://github.com/owner/repo.git" {
		t.Errorf("header: %q %q %q", s.Title, s.GitBranch, s.GitRemote)
	}
	if !s.Ref.StartedAt.Equal(time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("started: %v", s.Ref.StartedAt)
	}
	if len(s.Turns) != 3 {
		t.Fatalf("turns: %d %+v", len(s.Turns), s.Turns)
	}
	if u := s.Turns[0]; u.Role != "user" || !strings.HasPrefix(u.Text, "build the widget") || strings.Contains(u.Text, "ADDITIONAL_METADATA") ||
		strings.Contains(u.Text, "local time") || strings.Contains(u.Text, "model changed") {
		t.Errorf("user text keeps only the request: %.80q", u.Text)
	}
	if u := s.Turns[2]; u.Text != "unclosed request" {
		t.Errorf("an unclosed request stops at the metadata: %q", u.Text)
	}
	as := s.Turns[1]
	if as.Role != "assistant" || as.LastText != "Done: the widget is committed." || strings.Contains(as.Text, "SECRET-THINKING") || strings.Contains(as.Text, "CHECKPOINT-SUMMARY") {
		t.Errorf("assistant: %+v", as)
	}
	if len(as.Tools) != 9 {
		t.Fatalf("tools: %d", len(as.Tools))
	}
	commit, read, edit, write, failed, unanswered := as.Tools[0], as.Tools[1], as.Tools[2], as.Tools[3], as.Tools[4], as.Tools[5]
	if commit.Kind != model.ToolShell || commit.Command != "git commit -m widget" || !commit.Result.Known || !commit.Result.OK ||
		commit.Result.ExitCode == nil || *commit.Result.ExitCode != 0 || !strings.Contains(commit.Result.Text, "abc1234") || strings.Contains(commit.Result.Text, "Created At") {
		t.Errorf("commit: %+v", commit)
	}
	if read.Kind != model.ToolFileRead || read.Path != "/home/u/code/my app/main.go" {
		t.Errorf("read: %+v", read)
	}
	if edit.Kind != model.ToolFileEdit || !edit.Result.Known || !edit.Result.OK {
		t.Errorf("edit: %+v", edit)
	}
	if write.Kind != model.ToolFileWrite || !write.Created || !write.Result.OK {
		t.Errorf("write: %+v", write)
	}
	if !failed.Result.Known || failed.Result.OK {
		t.Errorf("an ERROR result is a known failure: %+v", failed.Result)
	}
	if unanswered.Result.Known {
		t.Errorf("a call without a result is unknown: %+v", unanswered.Result)
	}
	// the next round pairs its own results: the unanswered call above does not shift them
	daemon, invalid, vet := as.Tools[6], as.Tools[7], as.Tools[8]
	if daemon.Result.Known || !strings.Contains(daemon.Result.Text, "background") {
		t.Errorf("a background command is unknown: %+v", daemon.Result)
	}
	if !invalid.Result.Known || invalid.Result.OK {
		t.Errorf("an INVALID result is a known failure: %+v", invalid.Result)
	}
	if !vet.Result.Known || vet.Result.OK || vet.Result.ExitCode == nil || *vet.Result.ExitCode != 2 {
		t.Errorf("vet: %+v", vet.Result)
	}
	if !slices.Equal(s.FilesTouched, []string{"main.go", "new.go"}) {
		t.Errorf("files: only successful edits and writes, once each: %v", s.FilesTouched)
	}
	if s.ToolsUsed["run_command"] != 4 || s.ToolsUsed["edit_file"] != 3 {
		t.Errorf("tools used: %v", s.ToolsUsed)
	}
	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "unknown type") {
		t.Errorf("warnings: %v", s.Warnings)
	}
}

func TestLoadRefusesWhatItCannotRead(t *testing.T) {
	a, ide, _ := setup(t)
	refs, _ := list(t, a, adapters.ListOpts{})
	var ref model.SessionRef
	for _, r := range refs {
		if r.ID == idMain {
			ref = r
		}
	}
	p := transcript(t, ide, idMain, user("2026-11-21T10:00:00Z", "ok"))
	if err := os.WriteFile(p, append(must(os.ReadFile(p)), []byte("{broken\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Load(context.Background(), ref); !adapters.IsSkip(err) {
		t.Errorf("a corrupt line skips the session: %v", err)
	}
	// a path the adapter did not list is refused
	bad := ref
	bad.Path = filepath.Join(t.TempDir(), "transcript_full.jsonl")
	if _, err := a.Load(context.Background(), bad); !adapters.IsSkip(err) {
		t.Errorf("foreign path: %v", err)
	}
	// without metadata the branch and remote stay empty
	transcript(t, ide, idMain, user("2026-11-21T10:00:00Z", "ok"))
	s, err := a.Load(context.Background(), ref)
	if err != nil || s.GitBranch != "" || s.GitRemote != "" {
		t.Errorf("no metadata: %+v %v", s, err)
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}
