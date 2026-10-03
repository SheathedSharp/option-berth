package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRootsRoundTrip(t *testing.T) {
	s := openTemp(t)

	roots, err := s.Roots()
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if len(roots) != 0 {
		t.Fatalf("fresh store has roots %v", roots)
	}

	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	for _, p := range []string{a, b} {
		// A root is recorded while it is there — a real one is a directory
		// with a oberth.yaml in it.
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := s.AddRoot(p); err != nil {
			t.Fatalf("AddRoot(%s): %v", p, err)
		}
	}
	// Adding the same root twice is a no-op, trailing separators and all.
	if err := s.AddRoot(a + string(filepath.Separator)); err != nil {
		t.Fatalf("AddRoot (duplicate): %v", err)
	}

	roots, err = s.Roots()
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	// Stored resolved: on macOS t.TempDir() hands out /var/... and the store
	// keeps /private/var/..., which is the whole point of cleanRoot.
	if want := []string{resolved(t, a), resolved(t, b)}; !reflect.DeepEqual(roots, want) {
		t.Errorf("Roots = %v, want %v", roots, want)
	}

	if err := s.RemoveRoot(a); err != nil {
		t.Fatalf("RemoveRoot: %v", err)
	}
	roots, _ = s.Roots()
	if want := []string{resolved(t, b)}; !reflect.DeepEqual(roots, want) {
		t.Errorf("Roots after remove = %v, want %v", roots, want)
	}
	// Removing an unknown root is not an error.
	if err := s.RemoveRoot(a); err != nil {
		t.Errorf("second RemoveRoot: %v", err)
	}
}

// TestOneDirectoryIsOneRow is E5: the same directory reached by two spellings
// of its path used to be two rows, and since RemoveRoot deletes by string, the
// spelling you did not add could never be removed.
func TestOneDirectoryIsOneRow(t *testing.T) {
	s := openTemp(t)
	base := t.TempDir()
	real := filepath.Join(base, "checkout")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlinked spelling of the same directory, made here so the test does
	// not depend on the machine having one (macOS has: /var → /private/var).
	link := filepath.Join(base, "shortcut")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}

	for _, p := range []string{real, link} {
		if err := s.AddRoot(p); err != nil {
			t.Fatalf("AddRoot(%s): %v", p, err)
		}
	}
	roots, err := s.Roots()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{resolved(t, real)}; !reflect.DeepEqual(roots, want) {
		t.Fatalf("Roots = %v, want %v — one row per directory", roots, want)
	}

	// And either spelling deletes it.
	if err := s.RemoveRoot(link); err != nil {
		t.Fatalf("RemoveRoot(%s): %v", link, err)
	}
	if roots, _ = s.Roots(); len(roots) != 0 {
		t.Errorf("Roots = %v after removing the other spelling, want none", roots)
	}
}

// TestRootOfAVanishedDirectoryCanStillBeForgotten: the seed step calls
// RemoveRoot for directories that are already gone (a moved checkout, a test's
// temp dir), and EvalSymlinks fails on a path that does not exist. The
// resolution has to walk down to the deepest part that is still there, or a
// vanished root would be unremovable — the exact bug that made this table grow
// forever.
func TestRootOfAVanishedDirectoryCanStillBeForgotten(t *testing.T) {
	s := openTemp(t)
	gone := filepath.Join(t.TempDir(), "moved-away")

	if err := s.AddRoot(gone); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}
	roots, err := s.Roots()
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 {
		t.Fatalf("Roots = %v, want the vanished path recorded", roots)
	}

	// The same missing directory, reached through the resolved spelling of the
	// parent — which on macOS is what /var against /private/var does to every
	// temp path.
	resolvedParent := resolved(t, filepath.Dir(gone))
	other := filepath.Join(resolvedParent, "moved-away")
	if err := s.RemoveRoot(other); err != nil {
		t.Fatalf("RemoveRoot(%s): %v", other, err)
	}
	if roots, _ = s.Roots(); len(roots) != 0 {
		t.Errorf("Roots = %v, want the vanished root removed", roots)
	}
}

// resolved is the spelling the store keeps: symlinks resolved for the part of
// the path that exists.
func resolved(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return real
}

func TestRootsAreAbsolute(t *testing.T) {
	s := openTemp(t)

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := s.AddRoot("testdata"); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}
	roots, err := s.Roots()
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if want := []string{filepath.Join(wd, "testdata")}; !reflect.DeepEqual(roots, want) {
		// testdata is checked in, so this is also the symlink-resolved
		// spelling of the working directory.
		t.Errorf("Roots = %v, want %v", roots, want)
	}

	if err := s.AddRoot("  "); err == nil {
		t.Error("AddRoot accepted an empty path")
	}
	if err := s.RemoveRoot(""); err == nil {
		t.Error("RemoveRoot accepted an empty path")
	}
}

func TestRootsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "option-berth.db")
	root := t.TempDir()

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := first.AddRoot(root); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = second.Close() }()

	roots, err := second.Roots()
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if len(roots) != 1 || roots[0] != resolved(t, root) {
		t.Errorf("Roots after reopen = %v, want [%s]", roots, resolved(t, root))
	}
}
