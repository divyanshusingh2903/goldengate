package e2e

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	_ "modernc.org/sqlite"
)

// An Antigravity conversation in a shared folder reaches the server with
// its branch, its prompt without the harness metadata, and its commit
// proven by the exit code (#200). Everything here is made up.
func TestAntigravitySessionReachesTheServer(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	l.antigravity = filepath.Join(l.root, "antigravity")
	id := "77777777-7777-4777-8777-777777777777"
	ctx := context.Background()

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(l.antigravity, "conversations"), 0o700))
	idx, err := sql.Open("sqlite", "file:"+filepath.Join(l.antigravity, "conversation_summaries.db"))
	must(err)
	_, err = idx.ExecContext(ctx, `CREATE TABLE conversation_summaries (conversation_id text PRIMARY KEY, title text NOT NULL DEFAULT '',
		last_modified_time datetime NOT NULL DEFAULT '', workspace_uris text NOT NULL DEFAULT '', parent_conversation_id text NOT NULL DEFAULT '',
		nesting_depth integer NOT NULL DEFAULT 0, app_data_dir text NOT NULL DEFAULT '')`)
	must(err)
	_, err = idx.ExecContext(ctx, `INSERT INTO conversation_summaries(conversation_id, title, workspace_uris) VALUES(?, 'Ship the gadget', '["file:///home/user/projects/demo"]')`, id)
	must(err)
	must(idx.Close())

	var ws, blob []byte
	ws = protowire.AppendTag(ws, 4, protowire.BytesType)
	ws = protowire.AppendString(ws, "feat/GADGET-branch")
	blob = protowire.AppendTag(blob, 1, protowire.BytesType)
	blob = protowire.AppendBytes(blob, ws)
	meta, err := sql.Open("sqlite", "file:"+filepath.Join(l.antigravity, "conversations", id+".db"))
	must(err)
	_, err = meta.ExecContext(ctx, `CREATE TABLE trajectory_metadata_blob (id text DEFAULT 'main', data blob, PRIMARY KEY (id))`)
	must(err)
	_, err = meta.ExecContext(ctx, `INSERT INTO trajectory_metadata_blob(id, data) VALUES('main', ?)`, blob)
	must(err)
	must(meta.Close())

	logs := filepath.Join(l.antigravity, "brain", id, ".system_generated", "logs")
	must(os.MkdirAll(logs, 0o700))
	transcript := strings.Join([]string{
		`{"type":"USER_INPUT","status":"DONE","created_at":"2026-01-02T09:00:00Z","content":"<USER_REQUEST>\nplease ship the GADGET\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nHARNESS-METADATA\n</ADDITIONAL_METADATA>"}`,
		`{"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-01-02T09:00:05Z","thinking":"PRIVATE-THINKING","tool_calls":[{"name":"run_command","args":{"CommandLine":"git commit -m gadget"}}]}`,
		`{"type":"GENERIC","status":"DONE","created_at":"2026-01-02T09:00:06Z","content":"Created At: x\nCompleted At: y\nCommand exited with code 0.\n\nCommand output:\n[feat/GADGET-branch 1a2b3c4] gadget"}`,
		`{"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-01-02T09:00:10Z","content":"The GADGET is committed."}`,
	}, "\n") + "\n"
	must(os.WriteFile(filepath.Join(logs, "transcript_full.jsonl"), []byte(transcript), 0o600))

	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo", "--auto-approve")
	l.must(nil, "sync")
	held := everythingTheServerHolds(t, b)
	for _, want := range []string{`"source":"antigravity"`, "feat/GADGET-branch", "GADGET"} {
		if !strings.Contains(held, want) {
			t.Errorf("the server does not hold %q", want)
		}
	}
	for _, never := range []string{"HARNESS-METADATA", "PRIVATE-THINKING"} {
		if strings.Contains(held, never) {
			t.Errorf("%q reached the server", never)
		}
	}
}
