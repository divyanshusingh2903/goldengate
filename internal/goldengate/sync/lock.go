package sync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// ErrBusy is returned when another send holds the run lock.
var ErrBusy = errors.New("another send is running")

// lockName is the run lock's file in the state directory.
const lockName = "sync.lock"

// RunLock keeps a second send (a timer, a hook, the daemon, someone pressing
// Send) from running while one is under way, so no session is sent twice and
// the backoff record is written by one process at a time.
//
// It is an OS file lock, released by the operating system when the process
// ends, however it ends: there is no stale lock to guess about. The file is
// never removed: removing a locked file lets two processes lock two
// different files of the same name.
type RunLock struct{ f *os.File }

// AcquireRunLock takes the run lock in dir without waiting. It returns
// ErrBusy when another send holds it.
func AcquireRunLock(dir string) (*RunLock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("lock: mkdir: %w", err)
	}
	path := filepath.Join(dir, lockName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: our own state directory
	if err != nil {
		return nil, fmt.Errorf("lock: open %s: %w", path, err)
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errWouldBlock) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("lock: %w", err)
	}
	// Who holds it, for a person looking at the file; nothing reads it.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+" "+time.Now().UTC().Format(time.RFC3339)+"\n"), 0)
	}
	return &RunLock{f: f}, nil
}

// WaitRunLock takes the run lock in dir, waiting up to wait for another send
// to finish. It returns ErrBusy if that send is still running then.
func WaitRunLock(ctx context.Context, dir string, wait time.Duration) (*RunLock, error) {
	deadline := time.Now().Add(wait)
	for {
		l, err := AcquireRunLock(dir)
		if !errors.Is(err, ErrBusy) || !time.Now().Before(deadline) {
			return l, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Release lets the next send run. It is safe to call on nil.
func (l *RunLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unlockFile(l.f)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}
