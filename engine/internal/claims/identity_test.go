package claims_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/claims"
)

// tree creates a directory tree under a fresh temp root and returns the root.
// A case names the shape it needs; nothing here repeats the fixture.
func tree(t *testing.T, entries ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, entry := range entries {
		if err := os.MkdirAll(filepath.Join(root, entry), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestIdentityFromACheckout covers the two shapes a working directory can have
// inside a repository: the checkout root, and a directory below it. Both name
// the same project and worktree, because a service's cwd may be a subdirectory
// of the manifest.
func TestIdentityFromACheckout(t *testing.T) {
	root := tree(t, "myapp/.git", "myapp/web")

	for _, dir := range []string{filepath.Join(root, "myapp"), filepath.Join(root, "myapp", "web")} {
		project, worktree := claims.Identity(dir)
		if project != "myapp" || worktree != claims.DefaultWorktree {
			t.Errorf("Identity(%q) = %q/%q, want myapp/%s", dir, project, worktree, claims.DefaultWorktree)
		}
	}
}

func TestIdentityOutsideARepository(t *testing.T) {
	root := tree(t, "loose")
	project, worktree := claims.Identity(filepath.Join(root, "loose"))
	if project != "loose" || worktree != claims.DefaultWorktree {
		t.Errorf("Identity(no repo) = %q/%q, want loose/%s", project, worktree, claims.DefaultWorktree)
	}
}

// A directory with no name of its own still yields a project, so a key is never
// empty.
func TestIdentityOfADirectoryWithNoName(t *testing.T) {
	project, worktree := claims.Identity("/")
	if project == "" || worktree != claims.DefaultWorktree {
		t.Errorf("Identity(\"/\") = %q/%q, want a named project and %s", project, worktree, claims.DefaultWorktree)
	}
}

// A linked worktree keeps the repository as the project and the checkout as the
// worktree, so sibling worktrees of one repo get different addresses.
func TestIdentityInALinkedWorktree(t *testing.T) {
	root := tree(t, "myapp/.git/worktrees/feature-x", "myapp-feature-x")
	gitfile := "gitdir: " + filepath.Join(root, "myapp", ".git", "worktrees", "feature-x") + "\n"
	if err := os.WriteFile(filepath.Join(root, "myapp-feature-x", ".git"), []byte(gitfile), 0o644); err != nil {
		t.Fatal(err)
	}

	// groups.Find names a linked worktree "<repo>@<checkout dir>", so the
	// worktree half is the directory the agent is working in.
	project, worktree := claims.Identity(filepath.Join(root, "myapp-feature-x"))
	if project != "myapp" || worktree != "myapp-feature-x" {
		t.Errorf("Identity(linked worktree) = %q/%q, want myapp/myapp-feature-x", project, worktree)
	}
	if key := (claims.Address{Project: project, Worktree: worktree, Service: "api"}).Key(); key != "myapp/myapp-feature-x/api" {
		t.Errorf("key = %q, want myapp/myapp-feature-x/api", key)
	}
}
