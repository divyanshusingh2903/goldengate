package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
)

// appendConfig adds TOML to the end of the laptop's config file.
func (l *laptop) appendConfig(toml string) {
	l.t.Helper()
	path := filepath.Join(l.root, "cfg", "goldengate", "config.toml")
	b, err := os.ReadFile(path)
	if err != nil {
		l.t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, []byte("\n"+toml)...), 0o600); err != nil {
		l.t.Fatal(err)
	}
}

// touchSessions sets every Claude Code transcript's modification time.
func (l *laptop) touchSessions(at time.Time) {
	l.t.Helper()
	err := filepath.Walk(filepath.Join(l.claude, "projects"), func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		return os.Chtimes(p, at, at)
	})
	if err != nil {
		l.t.Fatal(err)
	}
}

func (l *laptop) autoLine(args ...string) string {
	l.t.Helper()
	l.errOut.Reset()
	l.must(nil, append([]string{"sync", "--auto"}, args...)...)
	return strings.TrimSpace(l.errOut.String())
}

// An unattended run, which may come every few minutes, leaves a session
// alone while it is still being written, sends nothing while another send
// runs, and backs off from a server it cannot reach (#188).
func TestUnattendedSyncSettlesLocksAndBacksOff(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	now := time.Now()
	l.now = func() time.Time { return now }
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo", "--auto-approve")

	// just written: held back, not recorded, nothing on the server
	l.touchSessions(now.Add(-2 * time.Minute))
	if line := l.autoLine(); !strings.Contains(line, "nothing new was sent") || !strings.Contains(line, "1 session(s) changed too recently and wait until they settle") {
		t.Fatalf("report: %q", line)
	}
	if strings.Contains(everythingTheServerHolds(t, b), "widget") {
		t.Fatal("a session still being written was sent")
	}
	// a dry run does not take the lock, check the server or settle anything differently
	l.must(nil, "sync", "--auto", "--dry-run")

	// ten minutes of quiet: the next run sends it
	l.touchSessions(now.Add(-11 * time.Minute))
	if line := l.autoLine(); !strings.Contains(line, "1 session(s) sent, listed in receipt") || strings.Contains(line, "settle") {
		t.Fatalf("report: %q", line)
	}
	if !strings.Contains(everythingTheServerHolds(t, b), "widget") {
		t.Fatal("the settled session is sent")
	}

	// a send already running: the unattended run does nothing and says so
	l.addClaudeSession("33333333-3333-4333-8333-333333333333", "/home/user/projects/demo", "LOCKED work")
	l.touchSessions(now.Add(-11 * time.Minute))
	lock, err := gsync.AcquireRunLock(filepath.Join(l.root, "state", "goldengate"))
	if err != nil {
		t.Fatal(err)
	}
	if line := l.autoLine(); line != "goldengate background send: another send is running; nothing done" {
		t.Fatalf("report: %q", line)
	}
	if strings.Contains(everythingTheServerHolds(t, b), "LOCKED") {
		t.Fatal("sent while another send held the lock")
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}

	// the server cannot be reached: one line, success for the timer, and
	// the next runs leave it alone for a while
	b.down.Store(true)
	if line := l.autoLine(); !strings.Contains(line, "the server is not reachable") || !strings.Contains(line, "nothing sent, next try after") {
		t.Fatalf("report: %q", line)
	}
	now = now.Add(30 * time.Second)
	if line := l.autoLine(); !strings.Contains(line, "was not reachable at the last 1 attempt(s)") {
		t.Fatalf("within the backoff the server is not tried: %q", line)
	}
	now = now.Add(time.Minute) // past the first wait; still down: the wait doubles
	if line := l.autoLine(); !strings.Contains(line, "the server is not reachable") {
		t.Fatalf("report: %q", line)
	}
	now = now.Add(time.Minute)
	if line := l.autoLine(); !strings.Contains(line, "at the last 2 attempt(s)") {
		t.Fatalf("the second wait is two minutes: %q", line)
	}
	// back up: the run after the wait sends, and the record is cleared
	b.down.Store(false)
	now = now.Add(2 * time.Minute)
	if line := l.autoLine(); !strings.Contains(line, "1 session(s) sent, listed in receipt") {
		t.Fatalf("report: %q", line)
	}
	if !strings.Contains(everythingTheServerHolds(t, b), "LOCKED") {
		t.Fatal("the session is sent once the server is back")
	}
	b.down.Store(true)
	if line := l.autoLine(); !strings.Contains(line, "the server is not reachable") {
		t.Fatalf("after a success the server is tried at once: %q", line)
	}

	// the Claude Code hook runs when a session has ended: --settle 0 sends
	// it at once; --settle means nothing without --auto
	b.down.Store(false)
	now = now.Add(2 * time.Minute)
	l.addClaudeSession("44444444-4444-4444-8444-444444444444", "/home/user/projects/demo", "HOOKED work")
	l.touchSessions(now)
	if line := l.autoLine("--settle", "0"); !strings.Contains(line, "1 session(s) sent") {
		t.Fatalf("--settle 0: %q", line)
	}
	if err := l.run(nil, "sync", "--settle", "0"); err == nil || !strings.Contains(err.Error(), "--auto") {
		t.Fatalf("--settle without --auto: %v", err)
	}
}

// A session already on the server that keeps changing is refreshed at most
// once per gap by unattended runs; --resend-gap 0 (the Claude Code hook) and
// the gap running out send it (#197).
func TestUnattendedSyncResendsOnlyAfterTheGap(t *testing.T) {
	b := startBridge(t)
	l := newLaptop(t)
	now := time.Now()
	l.now = func() time.Time { return now }
	l.must(l.initEnv(b), "init", "--non-interactive")
	l.must(nil, "share", "add", "~/projects/demo", "--auto-approve")

	id := "55555555-5555-4555-8555-555555555555"
	l.addClaudeSession(id, "/home/user/projects/demo", "FIRSTDRAFT work")
	l.touchSessions(now.Add(-30 * time.Minute))
	if line := l.autoLine(); !strings.Contains(line, "session(s) sent") {
		t.Fatalf("first send: %q", line)
	}

	// the session grows an hour later: sent recently, so it waits
	now = now.Add(time.Hour)
	l.addClaudeSession(id, "/home/user/projects/demo", "SECONDDRAFT work")
	l.touchSessions(now.Add(-30 * time.Minute))
	line := l.autoLine()
	if !strings.Contains(line, "nothing new was sent") || !strings.Contains(line, "changed session(s) were sent recently and are sent again later") {
		t.Fatalf("held: %q", line)
	}
	if strings.Contains(everythingTheServerHolds(t, b), "SECONDDRAFT") {
		t.Fatal("a session sent an hour ago was sent again")
	}
	// the hook sends it at once
	if line := l.autoLine("--resend-gap", "0"); !strings.Contains(line, "session(s) sent") {
		t.Fatalf("--resend-gap 0: %q", line)
	}
	if err := l.run(nil, "sync", "--resend-gap", "0"); err == nil || !strings.Contains(err.Error(), "--auto") {
		t.Fatalf("--resend-gap without --auto: %v", err)
	}

	// and with the gap switched off in the settings, an hour is enough
	l.appendConfig("[sync]\nresend_minutes = 0\n")
	now = now.Add(time.Hour)
	l.addClaudeSession(id, "/home/user/projects/demo", "THIRDDRAFT work")
	l.touchSessions(now.Add(-30 * time.Minute))
	if line := l.autoLine(); !strings.Contains(line, "session(s) sent") {
		t.Fatalf("gap off: %q", line)
	}
}
