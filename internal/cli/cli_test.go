package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OWNER/goldengate/internal/adapters"
	"github.com/OWNER/goldengate/internal/adapters/claudecode"
	"github.com/OWNER/goldengate/internal/model"
)

const fixture = "../../testdata/claude-code/2.1/workitems.jsonl"

type harness struct {
	t            *testing.T
	out, errOut  *bytes.Buffer
	deps         Deps
	xdgCfg, home string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	h := &harness{t: t, home: "/home/user", xdgCfg: filepath.Join(root, "cfg")}
	claudeDir := filepath.Join(root, "claude")
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	put := func(project, id string, body string) {
		dir := filepath.Join(claudeDir, "projects", "-"+id)
		_ = os.MkdirAll(dir, 0o755)
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put("demo", "11111111-1111-4111-8111-111111111111", string(data))
	secret := strings.ReplaceAll(string(data), "/home/user/projects/demo", "/home/user/projects/client-secret")
	secret = strings.ReplaceAll(secret, "please ship the widget", "TOPSECRET-PROMPT "+"AKIA"+"IOSFODNN7EXAMPLE")
	put("secret", "22222222-2222-4222-8222-222222222222", secret)

	h.out, h.errOut = &bytes.Buffer{}, &bytes.Buffer{}
	env := map[string]string{"XDG_CONFIG_HOME": h.xdgCfg, "XDG_STATE_HOME": filepath.Join(root, "state")}
	h.deps = Deps{
		Stdout: h.out, Stderr: h.errOut, Home: h.home,
		Getenv: func(k string) string { return env[k] },
		NewRegistry: func(string, func(string) string) (*adapters.Registry, error) {
			var r adapters.Registry
			return &r, r.Register(claudecode.New(claudecode.Options{ConfigDir: claudeDir}))
		},
		Now: func() time.Time { return time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC) },
	}
	return h
}

func (h *harness) run(args ...string) error {
	h.out.Reset()
	h.errOut.Reset()
	cmd := New(h.deps)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(context.Background())
}

func TestDefaultDenySyncSendsNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix path fixtures")
	}
	h := newHarness(t)
	if err := h.run("sync", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(h.out.String()) != "" {
		t.Fatalf("nothing is selected, nothing may be printed: %s", h.out.String())
	}
	if !strings.Contains(h.errOut.String(), "0 would be sent") {
		t.Fatalf("report: %s", h.errOut.String())
	}
}

func TestShareAndDryRunOnlySelectedProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix path fixtures")
	}
	h := newHarness(t)
	if err := h.run("share", "add", "~/projects/demo"); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(h.xdgCfg, "goldengate", "config.toml")
	if st, err := os.Stat(cfgPath); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("config perms: %v %v", err, st)
	}
	if err := h.run("sync", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	out := h.out.String() + h.errOut.String()
	for _, leak := range []string{"client-secret", "TOPSECRET", "IOSFODNN7", "SECRET-DESCRIPTION", "SECRET-COMMENT"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q leaked:\n%s", leak, out)
		}
	}
	// stdout is a stream of JSON payloads: exactly one, for the selected project
	dec := json.NewDecoder(strings.NewReader(h.out.String()))
	var got []model.SessionPayload
	for dec.More() {
		var p model.SessionPayload
		if err := dec.Decode(&p); err != nil {
			t.Fatal(err)
		}
		got = append(got, p)
	}
	if len(got) != 1 || got[0].Project != "~/projects/demo" || got[0].Level != "summary" {
		t.Fatalf("payloads: %+v", got)
	}
	if got[0].Summary == nil || len(got[0].WorkItems) == 0 {
		t.Fatalf("expected a summary and work items: %+v", got[0])
	}
	// dry-run must not record anything: a second dry run prints the same payload again
	first := h.out.String()
	if err := h.run("sync", "--dry-run"); err != nil || h.out.String() != first {
		t.Fatalf("dry run is not repeatable: %v", err)
	}
}

func TestRealSyncNeedsInit(t *testing.T) {
	h := newHarness(t)
	_ = h.run("share", "add", "~/projects/demo")
	if err := h.run("sync"); err == nil || !strings.Contains(err.Error(), "goldengate init") {
		t.Fatalf("got %v", err)
	}
}

func TestShareListRemoveDeny(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix path fixtures")
	}
	h := newHarness(t)
	_ = h.run("share", "add", "~/projects")
	if err := h.run("share", "deny", "~/projects/client-secret"); err != nil {
		t.Fatal(err)
	}
	if err := h.run("share", "list", "--projects"); err != nil {
		t.Fatal(err)
	}
	out := h.out.String()
	if !strings.Contains(out, "[x] ~/projects/demo") || !strings.Contains(out, "[-] ~/projects/client-secret") {
		t.Fatalf("list:\n%s", out)
	}
	if err := h.run("share", "remove", "~/projects"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.out.String(), "1 project(s) un-shared") || strings.Contains(h.out.String(), "server") {
		t.Fatalf("remove must report tombstoned projects: %s", h.out.String())
	}
	if err := h.run("share", "remove", "~/projects"); err == nil {
		t.Fatal("removing a rule that is not there must fail")
	}
	if err := h.run("sync", "--dry-run"); err != nil || strings.TrimSpace(h.out.String()) != "" {
		t.Fatalf("after remove nothing may be sent: %v %s", err, h.out.String())
	}
}

func TestShareAddIdempotent(t *testing.T) {
	h := newHarness(t)
	_ = h.run("share", "add", "~/projects/demo")
	if err := h.run("share", "add", "~/projects/demo"); err != nil || !strings.Contains(h.out.String(), "already shared") {
		t.Fatalf("%v %s", err, h.out.String())
	}
}

func TestDoctor(t *testing.T) {
	h := newHarness(t)
	if err := h.run("doctor"); err != nil {
		t.Fatalf("%v\n%s", err, h.out.String())
	}
	out := h.out.String()
	if !strings.Contains(out, "claude-code") || !strings.Contains(out, "2 sessions") {
		t.Fatalf("doctor output:\n%s", out)
	}
}

func TestVersion(t *testing.T) {
	h := newHarness(t)
	if err := h.run("version"); err != nil || !strings.HasPrefix(h.out.String(), "goldengate") {
		t.Fatalf("%v %s", err, h.out.String())
	}
}
