package sync_test

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/divyanshusingh2903/goldengate/internal/goldengate/selection"
	gsync "github.com/divyanshusingh2903/goldengate/internal/goldengate/sync"
)

// A session that changed less than the settle window ago is held back by
// an unattended run without being read or recorded, and sent by a later
// run once it has been left alone.
func TestSettleHoldsSessionsStillBeingWritten(t *testing.T) {
	e := setup(t, selection.Policy{Allow: []string{"~/code/app"}}, sess("a", "/home/u/code/app", "build"))
	ctx := context.Background()
	now := t0.Add(time.Hour)
	e.s.Now = func() time.Time { return now }
	e.f.mtime = now.Add(-3 * time.Minute)
	sink := &capture{}
	rep, err := e.s.Run(ctx, gsync.RunOpts{Settle: 10 * time.Minute}, sink, false)
	if err != nil || len(sink.got) != 0 || rep.Settling != 1 || !rep.SettledAt.Equal(e.f.mtime.Add(10*time.Minute)) {
		t.Fatalf("held: %v %d %+v", err, len(sink.got), rep)
	}
	if e.f.loads["/home/u/code/app"] != 0 {
		t.Error("a held session was read")
	}
	if _, seen, _ := e.st.Get(ctx, "claude-code", "a"); seen {
		t.Error("a held session was recorded")
	}
	// without a settle window (someone pressing Send) it goes at once
	if _, rep, err := e.s.Plan(ctx, gsync.RunOpts{}); err != nil || rep.Settling != 0 || rep.Planned != 1 {
		t.Fatalf("no window: %v %+v", err, rep)
	}
	// ten minutes on, the same run sends it
	now = now.Add(8 * time.Minute)
	rep, err = e.s.Run(ctx, gsync.RunOpts{Settle: 10 * time.Minute}, sink, false)
	if err != nil || len(sink.got) != 1 || rep.Settling != 0 {
		t.Fatalf("settled: %v %d %+v", err, len(sink.got), rep)
	}
}

// A session already sent and unchanged since is unchanged, however recently
// it was sent: only new or changed sessions wait to settle.
func TestSettleDoesNotHoldUnchangedSessions(t *testing.T) {
	e := setup(t, selection.Policy{Allow: []string{"~/code/app"}}, sess("a", "/home/u/code/app", "build"))
	ctx := context.Background()
	now := t0.Add(time.Hour)
	e.s.Now = func() time.Time { return now }
	e.f.mtime = now.Add(-time.Minute)
	sink := &capture{}
	if _, err := e.s.Run(ctx, gsync.RunOpts{}, sink, false); err != nil || len(sink.got) != 1 {
		t.Fatalf("manual send: %v %d", err, len(sink.got))
	}
	rep, err := e.s.Run(ctx, gsync.RunOpts{Settle: 10 * time.Minute}, sink, false)
	if err != nil || rep.Unchanged != 1 || rep.Settling != 0 {
		t.Fatalf("unchanged: %v %+v", err, rep)
	}
	// it changes again: now it waits
	e.f.mtime = now.Add(-30 * time.Second)
	e.f.sessions[0].Ref.SizeBytes = 20
	rep, err = e.s.Run(ctx, gsync.RunOpts{Settle: 10 * time.Minute}, sink, false)
	if err != nil || rep.Settling != 1 || len(sink.got) != 1 {
		t.Fatalf("changed: %v %d %+v", err, len(sink.got), rep)
	}
}

func TestRunLockIsExclusiveAndReleased(t *testing.T) {
	dir := t.TempDir()
	l, err := gsync.AcquireRunLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gsync.AcquireRunLock(dir); !errors.Is(err, gsync.ErrBusy) {
		t.Fatalf("second holder: %v", err)
	}
	start := time.Now()
	if _, err := gsync.WaitRunLock(context.Background(), dir, 300*time.Millisecond); !errors.Is(err, gsync.ErrBusy) || time.Since(start) < 250*time.Millisecond {
		t.Fatalf("wait gives up after the wait: %v", err)
	}
	// released while someone waits: the waiter gets it
	go func() { time.Sleep(150 * time.Millisecond); _ = l.Release() }()
	l2, err := gsync.WaitRunLock(context.Background(), dir, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := l2.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sync.lock")); err != nil {
		t.Errorf("the lock file stays in place: %v", err)
	}
	var nilLock *gsync.RunLock
	if err := nilLock.Release(); err != nil {
		t.Error(err)
	}
}

// Of many sends started at once, exactly one holds the lock.
func TestRunLockOneWinnerUnderContention(t *testing.T) {
	dir := t.TempDir()
	for round := range 20 {
		start := make(chan struct{})
		got := make(chan *gsync.RunLock, 8)
		for range 8 {
			go func() {
				<-start
				l, err := gsync.AcquireRunLock(dir)
				if err != nil && !errors.Is(err, gsync.ErrBusy) {
					t.Error(err)
				}
				got <- l
			}()
		}
		close(start)
		var won []*gsync.RunLock
		for range 8 {
			if l := <-got; l != nil {
				won = append(won, l)
			}
		}
		if len(won) != 1 {
			t.Fatalf("round %d: %d holders", round, len(won))
		}
		_ = won[0].Release()
	}
}

func TestOfflineBackoffDoublesToAnHour(t *testing.T) {
	last := t0
	for n, want := range map[int]time.Duration{1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 6: 32 * time.Minute, 7: time.Hour, 50: time.Hour} {
		if got := (gsync.Offline{Failures: n, Last: last}).Next().Sub(last); got != want {
			t.Errorf("%d failures: wait %s, want %s", n, got, want)
		}
	}
	if !(gsync.Offline{}).Next().IsZero() {
		t.Error("no failures, no wait")
	}
	e := setup(t, selection.Policy{})
	ctx := context.Background()
	if err := e.st.SetOffline(ctx, gsync.Offline{Failures: 3, Last: last}); err != nil {
		t.Fatal(err)
	}
	if o, err := e.st.Offline(ctx); err != nil || o.Failures != 3 || !o.Last.Equal(last) {
		t.Fatalf("round trip: %v %+v", err, o)
	}
	if err := e.st.SetOffline(ctx, gsync.Offline{}); err != nil {
		t.Fatal(err)
	}
	if o, err := e.st.Offline(ctx); err != nil || o.Failures != 0 {
		t.Fatalf("cleared: %v %+v", err, o)
	}
}

// TestMain lets a test run this binary as a lock holder that never lets go.
func TestMain(m *testing.M) {
	if dir := os.Getenv("GOLDENGATE_TEST_HOLD_LOCK"); dir != "" {
		if _, err := gsync.AcquireRunLock(dir); err != nil {
			os.Exit(3)
		}
		_, _ = os.Stdout.WriteString("held\n")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A send that dies (killed, crashed, laptop lid closed mid-run) leaves no
// lock behind: the operating system releases it.
func TestRunLockFreedWhenHolderDies(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$") //nolint:gosec // G204: this test binary
	cmd.Env = append(os.Environ(), "GOLDENGATE_TEST_HOLD_LOCK="+dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "held\n" {
		_ = cmd.Process.Kill()
		t.Fatalf("holder: %q %v", line, err)
	}
	if _, err := gsync.AcquireRunLock(dir); !errors.Is(err, gsync.ErrBusy) {
		t.Fatalf("held by another process: %v", err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	l, err := gsync.AcquireRunLock(dir)
	if err != nil {
		t.Fatalf("after the holder died: %v", err)
	}
	_ = l.Release()
}
