//go:build !windows

package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLateReleaseCannotUnlinkSuccessorLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	first, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Pause the outgoing owner after its kernel unlock. The next owner can
	// already acquire the same inode before the outgoing Release completes.
	if err := unlockFile(first.f); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	current, err := os.Stat(path)
	if err != nil || !os.SameFile(original, current) {
		t.Fatalf("successor lock inode changed: %v", err)
	}
	third, err := AcquireLock(path)
	if err == nil {
		third.Release()
		t.Fatal("third owner entered while successor held the lock")
	}
	if !IsAlreadyRunning(err) {
		t.Fatal(err)
	}
}

func TestWaitForLockReleaseTrustsKernelNotDiagnosticPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	held, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if err := held.f.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := held.f.WriteAt([]byte("999999999\n"), 0); err != nil {
		t.Fatal(err)
	}
	if err := WaitForLockRelease(path, 30*time.Millisecond); err == nil {
		t.Fatal("stale diagnostic PID bypassed a held kernel lock")
	}
}
