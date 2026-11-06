package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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

// makeDirs creates each path as a directory.
func makeDirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("creating %s: %v", path, err)
		}
	}
}

// requireRoots asserts the whole roots table, naming the step that got there.
func requireRoots(t *testing.T, s *Store, step string, want ...string) {
	t.Helper()
	got, err := s.Roots()
	if err != nil {
		t.Fatalf("%s: Roots: %v", step, err)
	}
	if len(want) == 0 {
		if len(got) != 0 {
			t.Fatalf("%s: Roots = %v, want none", step, got)
		}
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: Roots = %v, want %v", step, got, want)
	}
}

func TestRootsRoundTrip(t *testing.T) {
	s := openTemp(t)
	requireRoots(t, s, "a fresh store")

	dir := t.TempDir()
	first, second := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	makeDirs(t, first, second)
	for _, root := range []string{first, second} {
		if err := s.AddRoot(root); err != nil {
			t.Fatalf("AddRoot(%s): %v", root, err)
		}
	}

	// Adding the same root again — trailing separator and all — is a no-op.
	if err := s.AddRoot(first + string(filepath.Separator)); err != nil {
		t.Fatalf("AddRoot (the same root again): %v", err)
	}
	// Roots come back resolved, which is what cleanRoot is for: on macOS
	// t.TempDir() hands out /var/… and the store keeps /private/var/….
	requireRoots(t, s, "after adding two", resolved(t, first), resolved(t, second))

	if err := s.RemoveRoot(first); err != nil {
		t.Fatalf("RemoveRoot: %v", err)
	}
	requireRoots(t, s, "after removing one", resolved(t, second))
	if err := s.RemoveRoot(first); err != nil {
		t.Errorf("removing an absent root = %v, want nil", err)
	}
}

// The same directory reached by two spellings of its path used to become two
// rows, and since RemoveRoot deletes by string, the spelling you did not add
// could never be removed.
func TestOneDirectoryIsOneRow(t *testing.T) {
	s := openTemp(t)
	base := t.TempDir()
	real := filepath.Join(base, "checkout")
	makeDirs(t, real)
	// A symlinked spelling, made here so the case does not depend on the
	// machine having one (macOS does: /var → /private/var).
	link := filepath.Join(base, "shortcut")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}

	for _, path := range []string{real, link} {
		if err := s.AddRoot(path); err != nil {
			t.Fatalf("AddRoot(%s): %v", path, err)
		}
	}
	requireRoots(t, s, "after both spellings", resolved(t, real))

	// Either spelling deletes it.
	if err := s.RemoveRoot(link); err != nil {
		t.Fatalf("RemoveRoot(%s): %v", link, err)
	}
	requireRoots(t, s, "after removing by the other spelling")
}

// A root whose directory is already gone must still be forgettable. The seed
// step calls RemoveRoot for checkouts that were moved or deleted, and
// EvalSymlinks fails on a path that does not exist — so resolution has to stop
// at the deepest part that is still there.
func TestTheRootOfAVanishedDirectoryCanStillBeForgotten(t *testing.T) {
	s := openTemp(t)
	gone := filepath.Join(t.TempDir(), "moved-away")

	if err := s.AddRoot(gone); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}
	// The parent still exists, so its symlinks resolve and the vanished tail is
	// kept as written — that is the whole point of resolving only the existing
	// prefix.
	kept := filepath.Join(resolved(t, filepath.Dir(gone)), "moved-away")
	requireRoots(t, s, "after recording a vanished path", kept)

	// The unresolved spelling of the same missing directory removes it too,
	// which is what /var against /private/var does to every temp path on macOS.
	if err := s.RemoveRoot(gone); err != nil {
		t.Fatalf("RemoveRoot(%s): %v", gone, err)
	}
	requireRoots(t, s, "after removing the vanished root")
}

func TestARootIsStoredAbsolute(t *testing.T) {
	s := openTemp(t)
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := s.AddRoot("testdata"); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}
	requireRoots(t, s, "a relative root", filepath.Join(workingDir, "testdata"))

	if err := s.AddRoot("  "); err == nil {
		t.Error("AddRoot accepted an empty path")
	}
	if err := s.RemoveRoot(""); err == nil {
		t.Error("RemoveRoot accepted an empty path")
	}
}

func TestRootsSurviveAReopen(t *testing.T) {
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
	t.Cleanup(func() { _ = second.Close() })
	requireRoots(t, second, "after reopen", resolved(t, root))
}
