package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A reset that lands in the same second as another one takes the next free
// suffix, starting at one rather than reusing the base name.
func TestFreeCorruptNameCountsCollisions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "option-berth.db")
	for attempt := 0; attempt < 100; attempt++ {
		stamp := path + ".corrupt-" + time.Now().UTC().Format("20060102-150405")
		if err := os.WriteFile(stamp, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := freeCorruptName(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(got, stamp) {
			// The second rolled over between the two clock reads; try again.
			_ = os.Remove(stamp)
			continue
		}
		if got != stamp+"-1" {
			t.Fatalf("freeCorruptName = %q, want %q", got, stamp+"-1")
		}
		return
	}
	t.Fatal("could not keep both timestamps in the same second")
}

// A stat failure that is not "does not exist" is reported, not retried as if
// the name were free.
func TestFreeCorruptNameReportsAStatFailure(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(file, "option-berth.db")

	done := make(chan error, 1)
	go func() {
		_, err := freeCorruptName(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a path under a regular file was treated as free")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("freeCorruptName kept retrying after a stat error")
	}
}
