package cli

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/claudecode"
	"github.com/divyanshusingh2903/goldengate/internal/goldengate/adapters/cursor"
)

// Cursor sessions appear under the same project as Claude Code sessions of
// the same folder, and follow the same sharing rules.
func TestCursorSessionsJoinTheirFoldersProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix path fixtures")
	}
	root := t.TempDir()
	claudeDir := filepath.Join(root, "claude")
	copyFile(t, "../../../testdata/claude-code/2.1/workitems.jsonl", filepath.Join(claudeDir, "projects", "-p", "11111111-1111-4111-8111-111111111111.jsonl"))
	userDir := filepath.Join(root, "Cursor", "User")
	if err := os.MkdirAll(filepath.Join(userDir, "globalStorage"), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(userDir, "globalStorage", "state.vscdb"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"schema.sql", "basic.sql"} {
		b, _ := os.ReadFile(filepath.Join("../../../testdata/cursor/3.30", f))
		if _, err := db.ExecContext(context.Background(), string(b)); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	m := &multi{t: t, root: root, out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	env := map[string]string{"XDG_CONFIG_HOME": filepath.Join(root, "cfg"), "XDG_STATE_HOME": filepath.Join(root, "state")}
	m.deps = Deps{
		Stdout: m.out, Stderr: m.errOut, Home: "/home/user", Getenv: func(k string) string { return env[k] },
		NewRegistry: func(string, func(string) string) (*adapters.Registry, error) {
			var r adapters.Registry
			for _, a := range []adapters.Adapter{claudecode.New(claudecode.Options{ConfigDir: claudeDir}),
				cursor.New(cursor.Options{UserDir: userDir, ProjectsDir: "../../../testdata/cursor/3.30/projects"})} {
				if err := r.Register(a); err != nil {
					return nil, err
				}
			}
			return &r, nil
		},
		Now: func() time.Time { return time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC) },
	}

	// nothing is shared yet: no Cursor session is in a payload
	if err := m.run("sync", "--dry-run", "--approve"); err != nil {
		t.Fatal(err)
	}
	if len(m.payloads()) != 0 {
		t.Fatalf("default deny holds for Cursor too: %s", m.out)
	}
	// sharing the folder shares both tools' sessions of it, under one project
	if err := m.run("share", "add", "~/projects/demo"); err != nil {
		t.Fatal(err)
	}
	if err := m.run("sync", "--dry-run", "--approve"); err != nil {
		t.Fatal(err)
	}
	bySource := map[string]int{}
	projects := map[string]bool{}
	for _, p := range m.payloads() {
		bySource[p.Source]++
		projects[p.ProjectID+" "+p.ProjectName] = true
		if p.Source != "cursor" {
			continue
		}
		if p.Project != "~/projects/demo" || p.Summary == nil || p.Summary.Title == "" {
			t.Errorf("cursor payload: %+v", p)
		}
		// no command result from Cursor counts as proof
		for _, it := range p.WorkItems {
			if it.Status != "mentioned" {
				t.Errorf("a Cursor work item claims %s: %+v", it.Status, it)
			}
		}
	}
	if bySource["claude-code"] != 1 || bySource["cursor"] != 2 || len(projects) != 1 {
		t.Fatalf("both tools under one project: %v %v", bySource, projects)
	}
	out := m.out.String()
	for _, leak := range []string{"TOPSECRET", "A quick question", "rename the helper", "FUTURE-TEXT", "DRAFT-TEXT", "SUBAGENT-TEXT"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q reached a payload", leak)
		}
	}
	// the conversation in a future format is skipped with a warning, not guessed at
	if !strings.Contains(m.errOut.String(), "warning") && !strings.Contains(m.errOut.String(), "skipped") {
		t.Errorf("the unreadable conversation is reported: %s", m.errOut)
	}
	// a conversation without a folder is its own grouping, shared only by name
	if err := m.run("share", "add", "cursor:no-folder"); err != nil {
		t.Fatal(err)
	}
	if err := m.run("sync", "--dry-run", "--approve"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range m.payloads() {
		if p.Source == "cursor" && p.Project == "cursor:no-folder" {
			found = true
		}
	}
	if !found {
		t.Errorf("the no-folder grouping can be shared like a chat grouping: %s", m.out)
	}
}
