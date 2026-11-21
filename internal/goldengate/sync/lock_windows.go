//go:build windows

package sync

import (
	"os"

	"golang.org/x/sys/windows"
)

var errWouldBlock = windows.ERROR_LOCK_VIOLATION

// The lock covers one byte far past the end of the file, so the holder can
// still write who holds it at the start.
const lockOffset = 1 << 30

func lockFile(f *os.File) error {
	ol := &windows.Overlapped{Offset: lockOffset}
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
}

func unlockFile(f *os.File) error {
	ol := &windows.Overlapped{Offset: lockOffset}
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
