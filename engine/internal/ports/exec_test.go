package ports

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withCommandTimeout shortens the bound for one test and restores it after.
func withCommandTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	restore := commandTimeout
	commandTimeout = d
	t.Cleanup(func() { commandTimeout = restore })
}

// TestBoundedCommandTimesOut pins the wedge boundary: a command that never
// returns is killed at the bound instead of holding its caller forever.
func TestBoundedCommandTimesOut(t *testing.T) {
	withCommandTimeout(t, 100*time.Millisecond)

	cmd, stop := bounded("/bin/sh", "-c", "exec sleep 30")
	defer stop()

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a hung command returned success")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("hung command held for %v, want the 100ms bound", elapsed)
	}
}

// hungCommand writes an executable that never returns under name, and points
// PATH at it alone. The script's own children are absolute so the shim works
// with a PATH that holds nothing else.
func hungCommand(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nexec /bin/sleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// TestHungListenerCommandFailsTheScan: a wedged `lsof` used to freeze the
// daemon's only scan goroutine. It must surface as a failed scan — not as an
// empty healthy one, and not as an unbounded wait.
func TestHungListenerCommandFailsTheScan(t *testing.T) {
	withCommandTimeout(t, 100*time.Millisecond)
	hungCommand(t, "lsof")

	start := time.Now()
	rows, err := collectListeners("darwin", executeListenerCommand)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("a hung lsof produced %d rows and no error", len(rows))
	}
	if elapsed > 5*time.Second {
		t.Fatalf("hung lsof held for %v, want the 100ms bound", elapsed)
	}
}

// TestHungProcessTableYieldsNoIdentity: the enrichment table is a missing
// fact, not a failed scan, so a hung `ps` must come back empty within the
// bound rather than block the tick.
func TestHungProcessTableYieldsNoIdentity(t *testing.T) {
	withCommandTimeout(t, 100*time.Millisecond)
	hungCommand(t, "ps")

	start := time.Now()
	table := batchGetProcessTable()
	elapsed := time.Since(start)
	if len(table) != 0 {
		t.Fatalf("a hung ps produced %d rows", len(table))
	}
	if elapsed > 5*time.Second {
		t.Fatalf("hung ps held for %v, want the 100ms bound", elapsed)
	}
}
