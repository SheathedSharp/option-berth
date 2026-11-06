package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A second rename of the same project replaces the first rather than adding a
// row, and a trailing separator is the same root — the alias table is keyed by
// the cleaned path, so one checkout can only have one name.
func TestARenameReplacesTheAliasItAlreadyHad(t *testing.T) {
	s := openTemp(t)
	root := aliasFixtureRoot(t)

	if err := s.SetGroupAlias(root, "market"); err != nil {
		t.Fatalf("SetGroupAlias: %v", err)
	}
	if err := s.SetGroupAlias(root+string(os.PathSeparator), "store"); err != nil {
		t.Fatalf("SetGroupAlias again: %v", err)
	}
	aliases, err := s.GroupAliases()
	if err != nil {
		t.Fatalf("GroupAliases: %v", err)
	}
	if len(aliases) != 1 || aliases[root] != "store" {
		t.Fatalf("aliases = %v, want one cleaned root named store", aliases)
	}
}

// A name the product would not accept elsewhere is refused here too, and an
// empty root never reaches the table.
func TestAnUnusableAliasIsRefused(t *testing.T) {
	s := openTemp(t)
	if err := s.SetGroupAlias("/code/other", "a b"); !errors.Is(err, ErrInvalidName) {
		t.Errorf("an invalid name = %v, want ErrInvalidName", err)
	}
	if err := s.SetGroupAlias("", "x"); err == nil {
		t.Error("an empty root was accepted")
	}
	if aliases, _ := s.GroupAliases(); len(aliases) != 0 {
		t.Fatalf("a refused alias was stored: %v", aliases)
	}
}

// Clearing forgets the name, and clearing one that was never set is not an
// error: the seed step does not know which projects were ever renamed.
func TestClearingAnAliasIsIdempotent(t *testing.T) {
	s := openTemp(t)
	root := aliasFixtureRoot(t)
	if err := s.SetGroupAlias(root, "market"); err != nil {
		t.Fatalf("SetGroupAlias: %v", err)
	}

	if err := s.ClearGroupAlias(root); err != nil {
		t.Fatalf("ClearGroupAlias: %v", err)
	}
	if err := s.ClearGroupAlias("/code/never"); err != nil {
		t.Fatalf("clearing an absent alias = %v, want nil", err)
	}
	if aliases, _ := s.GroupAliases(); len(aliases) != 0 {
		t.Fatalf("aliases after clearing = %v, want none", aliases)
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
