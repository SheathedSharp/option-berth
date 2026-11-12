package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// lockPathInTemp hands back a lock file path inside a directory of its own.
func lockPathInTemp(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "daemon.lock")
}

func TestAcquireLockRefusesASecondInstance(t *testing.T) {
	path := lockPathInTemp(t)

	holder, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	defer holder.Release()

	// flock and LockFileEx both tie a lock to the open file description rather
	// than to the process, so a second AcquireLock is refused even when it
	// comes from inside this very test binary.
	rival, err := AcquireLock(path)
	if err == nil {
		rival.Release()
		t.Fatal("a second AcquireLock succeeded; the single-instance lock does not hold")
	}
	if !IsAlreadyRunning(err) {
		t.Fatalf("second AcquireLock returned %v, want ErrAlreadyRunning", err)
	}
	var clash *ErrAlreadyRunning
	if errors.As(err, &clash) && clash.PID != os.Getpid() {
		t.Errorf("ErrAlreadyRunning.PID = %d, want the holder %d", clash.PID, os.Getpid())
	}

	// The file names the holder's pid, which is how `oberth serve` can tell the
	// second instance who it lost to.
	if got := LockHolderPID(path); got != os.Getpid() {
		t.Errorf("LockHolderPID = %d, want %d", got, os.Getpid())
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the lock file: %v", err)
	}
	if recorded := strings.TrimSpace(string(raw)); recorded != strconv.Itoa(os.Getpid()) {
		t.Errorf("lock file holds %q, want the pid %d", raw, os.Getpid())
	}
}

func TestReleasePreservesTheLockFile(t *testing.T) {
	path := lockPathInTemp(t)
	held, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("acquiring the lock: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("releasing the lock: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the stable lock file went missing after Release: %v", err)
	}

	// Releasing frees the lock for the next taker.
	retaken, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("re-acquiring the released lock: %v", err)
	}
	retaken.Release()
}

func TestLockHolderPIDOnMissingFile(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.lock")
	if got := LockHolderPID(absent); got != 0 {
		t.Errorf("LockHolderPID on a missing file = %d, want 0", got)
	}
}

func TestErrAlreadyRunningMessage(t *testing.T) {
	clash := &ErrAlreadyRunning{PID: 6789}
	if got, want := clash.Error(), "daemon already running (pid 6789)"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !IsAlreadyRunning(clash) {
		t.Error("IsAlreadyRunning did not recognise its own error type")
	}
	if IsAlreadyRunning(os.ErrNotExist) {
		t.Error("IsAlreadyRunning claimed an unrelated error")
	}
}

// TestWaitForLockReleaseBlocksUntilTheHolderLetsGo covers the mechanism behind
// the `daemon restart` fix: a daemon closes its socket before it releases its
// lock, so its replacement has to wait for the lock to go.
func TestWaitForLockReleaseBlocksUntilTheHolderLetsGo(t *testing.T) {
	path := lockPathInTemp(t)
	held, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("acquiring the lock: %v", err)
	}

	const holdFor = 200 * time.Millisecond
	go func() {
		time.Sleep(holdFor)
		_ = held.Release()
	}()

	began := time.Now()
	if err := WaitForLockRelease(path, 10*time.Second); err != nil {
		t.Fatalf("waiting for the lock to be released: %v", err)
	}
	if waited := time.Since(began); waited < holdFor {
		t.Errorf("WaitForLockRelease returned after %s, before the holder released at %s", waited, holdFor)
	}

	// The lock is genuinely free, not merely reported free: take it.
	retaken, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("the lock WaitForLockRelease called free is still held: %v", err)
	}
	retaken.Release()
}

// TestWaitForLockReleaseTimesOutWithAClearError: a lock nobody ever releases
// has to produce a bounded, named failure instead of hanging.
func TestWaitForLockReleaseTimesOutWithAClearError(t *testing.T) {
	path := lockPathInTemp(t)
	held, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("acquiring the lock: %v", err)
	}
	defer held.Release()

	const budget = 100 * time.Millisecond
	err = WaitForLockRelease(path, budget)
	if err == nil {
		t.Fatal("WaitForLockRelease returned nil with the lock still held")
	}
	for _, needed := range []string{path, "still holds"} {
		if !strings.Contains(err.Error(), needed) {
			t.Errorf("error %q does not name %q", err, needed)
		}
	}
}

// TestWaitForLockReleaseIgnoresAStaleLockFile: a lock file left behind by a
// process that is long gone must not make a restart wait out the full timeout.
func TestWaitForLockReleaseIgnoresAStaleLockFile(t *testing.T) {
	path := lockPathInTemp(t)
	// A pid no process holds, and no lock held on the file at all.
	if err := os.WriteFile(path, []byte("2147483646\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WaitForLockRelease(path, time.Second); err != nil {
		t.Fatalf("WaitForLockRelease on a stale lock file: %v", err)
	}
}
