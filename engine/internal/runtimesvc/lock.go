package runtimesvc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrAlreadyRunning is what AcquireLock returns when another daemon holds the
// lock. When the lock file could be read, the message names the holder's pid.
type ErrAlreadyRunning struct {
	Path string
	PID  int
}

func (e *ErrAlreadyRunning) Error() string {
	if e.PID <= 0 {
		return "daemon already running"
	}
	return fmt.Sprintf("daemon already running (pid %d)", e.PID)
}

// IsAlreadyRunning reports whether err is, or wraps, ErrAlreadyRunning.
func IsAlreadyRunning(err error) bool {
	var target *ErrAlreadyRunning
	return errors.As(err, &target)
}

// Lock is the single-instance lock: an advisory lock (flock on Unix, LockFileEx
// on Windows) over <socket dir>/daemon.lock, holding the holder's pid in its
// contents so a second `oberth serve` can name the process it lost to.
//
// On Unix the whole file is locked; on Windows one byte far past its end is,
// since a Windows lock also blocks reads of the range it covers and the pid
// must stay readable while the lock is held (see lock_windows.go).
type Lock struct {
	f    *os.File
	path string
}

// AcquireLock takes the lock without blocking. It returns ErrAlreadyRunning
// when another process holds it.
func AcquireLock(path string) (*Lock, error) {
	err := os.MkdirAll(filepath.Dir(path), 0o700)
	if err != nil {
		return nil, fmt.Errorf("creating lock directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		if isLockedOpenError(err) {
			// Windows may refuse the open itself while another process holds
			// the file locked or opened without sharing, where Unix would let
			// us open it and refuse only the lock. The answer is the same —
			// somebody else has it — so it earns the same error, and callers
			// that retry (WaitForLockRelease) keep retrying.
			return nil, &ErrAlreadyRunning{PID: LockHolderPID(path), Path: path}
		}
		return nil, fmt.Errorf("opening lock file: %w", err)
	}
	// A daemon spawns children (`oberth start`, `oberth up`, autostart), and
	// none of them may inherit the lock handle: an inherited handle would keep
	// the lock — and on Windows the file itself — alive after the daemon that
	// took it has exited.
	markNotInheritable(f)

	err = lockFile(f)
	if err != nil {
		holderPID := readLockPID(f)
		f.Close()
		if errors.Is(err, errLockBusy) {
			return nil, &ErrAlreadyRunning{PID: holderPID, Path: path}
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}

	// The lock is ours now, so whatever pid was in the file is replaced by ours.
	if err = f.Truncate(0); err == nil {
		if _, writeErr := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); writeErr != nil {
			unlockFile(f)
			f.Close()
			return nil, fmt.Errorf("writing lock file: %w", writeErr)
		}
		_ = f.Sync()
	}
	return &Lock{path: path, f: f}, nil
}

// Release drops the kernel lock but preserves the inode. Removing its pathname
// could unlink a successor already holding that file and admit a third owner.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unlockFile(l.f)
	closeErr := l.f.Close()
	l.f = nil
	return errors.Join(err, closeErr)
}

// Path is the lock file's path.
func (l *Lock) Path() string { return l.path }

// WaitForLockRelease blocks until the daemon lock at path is free, then
// returns. It is what `oberth daemon restart` waits on: a daemon closes its
// socket well before it releases the lock, so waiting for the socket to stop
// accepting is not enough — the replacement daemon would start, fail to take
// the lock, exit "already running", and leave nothing running.
//
// The lock is taken and immediately released, which is the only honest test
// that it is free. File existence and its diagnostic PID do not prove lock
// ownership; the kernel releases a crashed process's lock automatically.
//
// On Windows the wait survives one further failure mode: opening a file the
// outgoing daemon still holds fails outright (ERROR_LOCK_VIOLATION or
// ERROR_SHARING_VIOLATION) instead of blocking. AcquireLock reports both as
// ErrAlreadyRunning, so this loop retries them rather than aborting the
// restart.
func WaitForLockRelease(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	var last error
	for {
		lock, err := AcquireLock(path)
		if err != nil {
			if !IsAlreadyRunning(err) {
				// Anything except "somebody holds it" (an unreadable
				// directory, say) will not fix itself by waiting.
				return err
			}
			last = err
			if !time.Now().Before(deadline) {
				return fmt.Errorf("the previous daemon still holds %s after %s: %w", path, timeout, last)
			}
			time.Sleep(25 * time.Millisecond)
			continue
		}
		_ = lock.Release()
		return nil
	}
}

// LockHolderPID reads the pid a lock file records. A missing or unreadable
// file yields 0; the value does not prove the process is alive.
func LockHolderPID(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	return pid
}

func readLockPID(f *os.File) int {
	raw := make([]byte, 32)
	n, _ := f.ReadAt(raw, 0)
	if n <= 0 {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw[:n])))
	if err != nil {
		return 0
	}
	return pid
}
