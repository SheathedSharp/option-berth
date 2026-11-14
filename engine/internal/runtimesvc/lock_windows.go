//go:build windows

package runtimesvc

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// errLockBusy marks the "another process holds it" case, which AcquireLock
// turns into ErrAlreadyRunning.
var errLockBusy = errors.New("lock held by another process")

// The locked region is a single byte at offset 1<<62, beyond any byte the lock
// file will ever hold.
//
// LockFileEx makes the range it locks unreadable to every other process, and
// locking the whole file therefore hid the one thing the file exists to
// publish: while a daemon ran, LockHolderPID and ErrAlreadyRunning.PID both
// came back 0 and os.ReadFile failed with "another process has locked a
// portion of the file". A range that cannot overlap the pid keeps the flock
// semantics wanted here — the lock lives on the open handle and dies with it —
// while leaving offset 0 readable to anyone asking whose daemon is running.
const (
	lockBytesHigh  = 0
	lockBytesLow   = 1
	lockOffsetHigh = 1 << 30 // (1<<30)<<32 == byte 1<<62 of the file
	lockOffsetLow  = 0
)

// lockRange is the region both LockFileEx and UnlockFileEx address: the offset
// travels in the OVERLAPPED, the length in the byte-count arguments.
func lockRange() *windows.Overlapped {
	return &windows.Overlapped{Offset: lockOffsetLow, OffsetHigh: lockOffsetHigh}
}

func lockFile(f *os.File) error {
	lockErr := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, lockBytesLow, lockBytesHigh, lockRange())
	if errors.Is(lockErr, windows.ERROR_LOCK_VIOLATION) || errors.Is(lockErr, windows.ERROR_IO_PENDING) {
		return errLockBusy
	}
	return lockErr
}

func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0,
		lockBytesLow, lockBytesHigh, lockRange())
}

// isLockedOpenError reports whether opening the lock file failed because
// another process holds it locked or open without sharing. Windows refuses the
// open itself where Unix refuses only the lock, and a restart must read that
// as "still held, try again" instead of as a broken lock directory.
func isLockedOpenError(cause error) bool {
	return errors.Is(cause, windows.ERROR_LOCK_VIOLATION) ||
		errors.Is(cause, windows.ERROR_SHARING_VIOLATION)
}

// markNotInheritable clears the handle's inherit flag, so no child the daemon
// starts can keep the lock (or, on Windows, the lock file) alive after the
// daemon exits. Go already opens files with O_CLOEXEC here; repeating it costs
// one syscall and states the requirement explicitly — a spawned child holding
// this handle is exactly how an exited daemon keeps looking like a running one.
func markNotInheritable(f *os.File) {
	_ = windows.SetHandleInformation(windows.Handle(f.Fd()), windows.HANDLE_FLAG_INHERIT, 0)
}
