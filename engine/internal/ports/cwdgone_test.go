package ports

import (
	"os"
	"testing"
)

// Where a path is (InTrash) and whether it is there at all are two facts. This
// is the second one: a process still holding a port for a directory that is
// gone.
func TestCwdGoneIsAboutWhetherThePathIsThere(t *testing.T) {
	dir := t.TempDir()
	if cwdGone(dir) {
		t.Errorf("cwdGone(%q) = true for a directory that exists", dir)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if !cwdGone(dir) {
		t.Errorf("cwdGone(%q) = false for a directory that was deleted", dir)
	}

	// Nobody ever learned the cwd: unknown is not the same as gone.
	if cwdGone("") {
		t.Error(`cwdGone("") = true, want false`)
	}
}
