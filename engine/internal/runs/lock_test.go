package runs

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWithLockDoesNotStealLiveOwner(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(lockPath()), 0o700); err != nil { t.Fatal(err) }
	if err := os.WriteFile(lockPath(), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil { t.Fatal(err) }
	started := time.Now()
	if err := withLock(func() error { t.Fatal("acquired a live lock"); return nil }); err == nil { t.Fatal("withLock stole a live owner lock") }
	if elapsed := time.Since(started); elapsed < 2*time.Second { t.Fatalf("live lock returned too early after %s", elapsed) }
	data, err := os.ReadFile(lockPath())
	if err != nil || string(data) != fmt.Sprintf("%d\n", os.Getpid()) { t.Fatalf("live lock changed: %q, %v", data, err) }
}

func TestWithLockReclaimsDeadOwner(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(lockPath()), 0o700); err != nil { t.Fatal(err) }
	if err := os.WriteFile(lockPath(), []byte("999999999\n"), 0o600); err != nil { t.Fatal(err) }
	old := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(lockPath(), old, old); err != nil { t.Fatal(err) }
	if err := withLock(func() error { return nil }); err != nil { t.Fatal(err) }
	if _, err := os.Stat(lockPath()); !os.IsNotExist(err) { t.Fatalf("lock file survived release: %v", err) }
}
