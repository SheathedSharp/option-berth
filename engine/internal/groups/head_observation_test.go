package groups

import (
	"os"
	"path/filepath"
	"testing"
)

// HEAD can change without changing its timestamp or length. This used to
// retain the previous branch forever in the daemon's Index.
func TestBranchReadsSameStampReplacement(t *testing.T) {
	c := newCheckouts(t, "project", "feature")
	co, _ := Locate(c.wt)
	x := NewIndex()
	path := filepath.Join(c.admin, "HEAD")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := x.Branch(co); got != "feature/x" {
		t.Fatal(got)
	}
	writeFile(t, path, "ref: refs/heads/feature/y\n")
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("fixture did not preserve metadata")
	}
	if got := x.Branch(co); got != "feature/y" {
		t.Fatalf("same-stamp HEAD retained old branch: %q", got)
	}
}
