//go:build !windows

package daemon

import (
	"errors"
	"os"
	"syscall"
)

// errLockBusy marks the "somebody else holds it" outcome, which AcquireLock
// turns into ErrAlreadyRunning rather than surfacing as an I/O failure.
var errLockBusy = errors.New("lock held by another process")

// lockFile takes an exclusive advisory lock without waiting. Two errnos mean
// the same thing here: EWOULDBLOCK on BSD-derived kernels, EAGAIN on Linux.
func lockFile(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, syscall.EAGAIN):
		return errLockBusy
	}
	return err
}

// unlockFile releases the advisory lock.
func unlockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

// isLockedOpenError is always false here: Unix opens the lock file and then
// refuses the lock, so a held lock never surfaces as an open failure.
func isLockedOpenError(error) bool { return false }

// markNotInheritable does nothing on Unix. Go opens every descriptor with
// O_CLOEXEC, so a forked child cannot inherit the lock's file at all.
func markNotInheritable(*os.File) {}
