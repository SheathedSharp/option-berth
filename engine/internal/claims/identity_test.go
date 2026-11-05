package claims_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/claims"
)

// checkout creates a directory that looks like a repository root and returns
// a path inside it.
func checkout(t *testing.T, name string) (root, inside string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), name)
	inside = filepath.Join(root, "web")
	for _, dir := range []string{filepath.Join(root, ".git"), inside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root, inside
}

func TestIdentityInsideACheckout(t *testing.T) {
	_, inside := checkout(t, "myapp")

	project, worktree := claims.Identity(inside, "", "")
	if project != "myapp" || worktree != claims.DefaultWorktree {
		t.Errorf("Identity = %q/%q, want myapp/%s", project, worktree, claims.DefaultWorktree)
	}
}

func TestExplicitIdentityWinsOverTheDirectory(t *testing.T) {
	_, inside := checkout(t, "myapp")
	project, worktree := claims.Identity(inside, "other", "feature")
	if project != "other" || worktree != "feature" {
		t.Errorf("Identity = %q/%q, want other/feature", project, worktree)
	}
}

func plainDirectory(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestIdentityOutsideARepository(t *testing.T) {
	project, worktree := claims.Identity(plainDirectory(t, "loose"), "", "")
	if project != "loose" || worktree != claims.DefaultWorktree {
		t.Errorf("Identity = %q/%q, want loose/%s", project, worktree, claims.DefaultWorktree)
	}
}

// A linked worktree keeps the repository as the project and the checkout
// directory as the worktree, so sibling worktrees claim different keys.
func TestIdentityInALinkedWorktree(t *testing.T) {
	base := t.TempDir()
	mainCheckout := filepath.Join(base, "myapp")
	gitDir := filepath.Join(mainCheckout, ".git", "worktrees", "feature-x")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(base, "myapp-feature-x")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	pointer := "gitdir: " + gitDir + "\n"
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte(pointer), 0o644); err != nil {
		t.Fatal(err)
	}

	project, worktree := claims.Identity(linked, "", "")
	if project != "myapp" || worktree != "myapp-feature-x" {
		t.Errorf("Identity = %q/%q, want myapp/myapp-feature-x", project, worktree)
	}
	if key := claims.Key(project, worktree); key != "myapp/myapp-feature-x" {
		t.Errorf("key = %q", key)
	}
}
