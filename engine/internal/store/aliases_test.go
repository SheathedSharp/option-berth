package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGroupAliases(t *testing.T) {
	s := openTemp(t)
	root := aliasFixtureRoot(t)

	if err := s.SetGroupAlias(root, "market"); err != nil {
		t.Fatalf("SetGroupAlias: %v", err)
	}
	if err := s.SetGroupAlias(root+string(os.PathSeparator), "store"); err != nil {
		t.Fatalf("SetGroupAlias again: %v", err)
	}
	got, err := s.GroupAliases()
	if err != nil {
		t.Fatalf("GroupAliases: %v", err)
	}
	if len(got) != 1 || got[root] != "store" {
		t.Fatalf("aliases = %v, want one cleaned root renamed to store", got)
	}

	if err := s.SetGroupAlias("/code/other", "a b"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("an invalid name = %v, want ErrInvalidName", err)
	}
	if err := s.SetGroupAlias("", "x"); err == nil {
		t.Error("an empty root was accepted")
	}

	if err := s.ClearGroupAlias(root); err != nil {
		t.Fatalf("ClearGroupAlias: %v", err)
	}
	if err := s.ClearGroupAlias("/code/never"); err != nil {
		t.Fatalf("clearing an absent alias: %v", err)
	}
	if got, _ := s.GroupAliases(); len(got) != 0 {
		t.Fatalf("aliases after clearing = %v", got)
	}
}

// Resolve the existing parent using the standard library, independently of
// store.cleanRoot: macOS /var links and Windows 8.3 temp aliases are not keys.
func aliasFixtureRoot(t *testing.T) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(parent, "shop")
}
