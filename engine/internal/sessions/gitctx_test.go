package sessions

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initRepository creates a real repository with one commit. The code under
// test reads .git directly, so a fake directory would prove nothing.
func initRepository(t *testing.T, dir string) {
	t.Helper()
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = dir
		command.Env = append(command.Environ(),
			"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
			"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		if output, err := command.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v\n%s", args, err, output)
		}
	}
	git("init", "-q", "-b", "main", ".")
	git("commit", "-q", "--allow-empty", "-m", "fixture")
}

func TestPrimaryCheckoutHasBranchButNoWorktreeName(t *testing.T) {
	root := t.TempDir()
	initRepository(t, root)

	worktree, branch := GitContext(root)
	if worktree != "" {
		t.Errorf("worktree = %q, want empty for a primary checkout", worktree)
	}
	if branch != "main" {
		t.Errorf("branch = %q, want main", branch)
	}
}

func TestGitContextResolvesFromASubdirectory(t *testing.T) {
	root := t.TempDir()
	initRepository(t, root)
	nested := filepath.Join(root, "pkg", "server")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, branch := GitContext(nested); branch != "main" {
		t.Errorf("branch = %q, want main", branch)
	}
}

func TestLinkedWorktreeIsNamedByItsDirectory(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	initRepository(t, root)

	linked := filepath.Join(parent, "feature-x")
	add := exec.Command("git", "worktree", "add", "-q", "-b", "feature/x", linked)
	add.Dir = root
	if output, err := add.CombinedOutput(); err != nil {
		t.Skipf("git worktree add: %v\n%s", err, output)
	}

	worktree, branch := GitContext(linked)
	if worktree != "feature-x" || branch != "feature/x" {
		t.Errorf("linked worktree = %q/%q, want feature-x/feature/x", worktree, branch)
	}
	if name, _ := GitContext(root); name != "" {
		t.Errorf("primary checkout reported worktree %q", name)
	}
}

func TestOutsideARepositoryThereIsNoContext(t *testing.T) {
	worktree, branch := GitContext(t.TempDir())
	if worktree != "" || branch != "" {
		t.Errorf("outside a repository = %q/%q, want empty", worktree, branch)
	}
}

func TestCaptureAddsTheGitContext(t *testing.T) {
	root := t.TempDir()
	initRepository(t, root)

	session, found := Capture(root, Options{Getenv: lookup(map[string]string{EnvSession: "claude-code:s1"})})
	if !found || session.ID != "s1" || session.Branch != "main" {
		t.Fatalf("Capture = %+v, %v", session, found)
	}
	if _, found := Capture(root, Options{
		Getenv:    lookup(nil),
		Processes: func() []Process { return nil },
	}); found {
		t.Error("Capture invented a session with no agent evidence")
	}
}

func TestBranchFromHEAD(t *testing.T) {
	cases := []struct {
		head string
		want string
	}{
		{"ref: refs/heads/main", "main"},
		{"ref: refs/heads/feature/x", "feature/x"},
		{"ref: refs/remotes/origin/main", "refs/remotes/origin/main"},
		{"0123456789abcdef0123456789abcdef01234567", "0123456789ab"},
		{"short", "short"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := branchFromHEAD(tc.head); got != tc.want {
			t.Errorf("branchFromHEAD(%q) = %q, want %q", tc.head, got, tc.want)
		}
	}
}

func TestGitDirPointerResolution(t *testing.T) {
	checkout := t.TempDir()
	pointer := filepath.Join(checkout, ".git")
	if err := os.WriteFile(pointer, []byte("gitdir: ../.git/worktrees/copy\n"), 0o644); err != nil {
		t.Fatalf("write pointer: %v", err)
	}
	want := filepath.Clean(filepath.Join(checkout, "../.git/worktrees/copy"))
	if got := gitDirFromFile(checkout, pointer); got != want {
		t.Errorf("gitdir = %q, want %q", got, want)
	}
}
