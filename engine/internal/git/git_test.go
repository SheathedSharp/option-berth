package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/groups"
)

// The sample below is copied from a real `git status --porcelain=v2 --branch -z`
// run (git 2.50.1), with the NULs written as \x00. Hand-written fixtures drift;
// this one is the format as it is.
func TestParseStatusReadsPorcelainV2(t *testing.T) {
	const out = "# branch.oid 909838b500dd6430e1ce436d22b2366a343d00cd\x00" +
		"# branch.head main\x00" +
		"# branch.upstream origin/main\x00" +
		"# branch.ab +3 -1\x00" +
		"1 M. N... 100644 100644 100644 aaa bbb a.txt\x00" +
		"2 R. N... 100644 100644 100644 aaa bbb R100 new.txt\x00old.txt\x00" +
		"u UU N... 100644 100644 100644 100644 aaa bbb ccc conflict.txt\x00" +
		"? b with space.txt\x00"

	var s Snapshot
	parseStatus([]byte(out), &s)

	if s.Head != "909838b500dd6430e1ce436d22b2366a343d00cd" {
		t.Errorf("head = %q", s.Head)
	}
	if s.Branch != "main" || s.Detached {
		t.Errorf("branch = %q detached = %v", s.Branch, s.Detached)
	}
	if s.Upstream != "origin/main" {
		t.Errorf("upstream = %q", s.Upstream)
	}
	if s.Ahead != 3 || s.Behind != 1 {
		t.Errorf("ahead/behind = %d/%d, want 3/1", s.Ahead, s.Behind)
	}
	// "M." and "R." are both staged, neither unstaged.
	if s.Staged != 2 || s.Unstaged != 0 {
		t.Errorf("staged/unstaged = %d/%d, want 2/0", s.Staged, s.Unstaged)
	}
	if s.Conflicts != 1 {
		t.Errorf("conflicts = %d, want 1", s.Conflicts)
	}
	if s.Untracked != 1 {
		t.Errorf("untracked = %d, want 1", s.Untracked)
	}
}

// A file with both staged and unstaged work counts in each column: that is what
// a person means by "this needs looking at".
func TestParseStatusCountsBothSides(t *testing.T) {
	const out = "1 MM N... 100644 100644 100644 aaa bbb both.txt\x00"
	var s Snapshot
	parseStatus([]byte(out), &s)
	if s.Staged != 1 || s.Unstaged != 1 {
		t.Fatalf("staged/unstaged = %d/%d, want 1/1", s.Staged, s.Unstaged)
	}
}

// Blocks are separated by an empty record, and a linked worktree says
// "detached" rather than naming a branch.
func TestParseWorktreesReadsBlocks(t *testing.T) {
	const out = "worktree /repo\x00HEAD aaa\x00branch refs/heads/main\x00\x00" +
		"worktree /repo-wt\x00HEAD bbb\x00detached\x00\x00" +
		"worktree /bare\x00bare\x00\x00"

	list := parseWorktrees([]byte(out))
	if len(list) != 3 {
		t.Fatalf("got %d worktrees, want 3", len(list))
	}
	if list[0].Path != "/repo" || list[0].Branch != "main" || list[0].Detached {
		t.Errorf("worktree 0 = %+v", list[0])
	}
	if list[1].Path != "/repo-wt" || !list[1].Detached || list[1].Branch != "" {
		t.Errorf("worktree 1 = %+v", list[1])
	}
	if !list[2].Bare {
		t.Errorf("worktree 2 = %+v, want bare", list[2])
	}
}

func TestParseCommitReadsTheFormat(t *testing.T) {
	out := "0b75edc\x00docs(memory): 修正长期约定\t记下三处\x002026-09-22T13:30:00+08:00\n"
	c := parseCommit([]byte(out))
	if c == nil {
		t.Fatal("parseCommit returned nil")
	}
	if c.Hash != "0b75edc" || c.Subject != "docs(memory): 修正长期约定\t记下三处" {
		t.Fatalf("commit = %+v", c)
	}
	if c.When != "2026-09-22T13:30:00+08:00" {
		t.Fatalf("when = %q", c.When)
	}
}

// Read end to end, against a repository built for the test: the only way to
// know the subprocess arguments and the parsing agree.
func TestReadAgainstARealRepository(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)

	write(t, dir, "a.txt", "hi\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")

	snap, err := Read(context.Background(), dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if snap.Branch == "" || snap.Detached {
		t.Fatalf("branch = %q detached = %v", snap.Branch, snap.Detached)
	}
	if snap.Head == "" {
		t.Error("head is empty")
	}
	if !snap.Clean() {
		t.Errorf("a fresh commit should be clean: %+v", snap)
	}
	if snap.Last == nil || snap.Last.Subject != "first" {
		t.Errorf("last = %+v, want subject \"first\"", snap.Last)
	}
	if len(snap.Worktrees) != 1 || !snap.Worktrees[0].Current {
		t.Errorf("worktrees = %+v, want one, current", snap.Worktrees)
	}

	// One modified file and one untracked.
	write(t, dir, "a.txt", "changed\n")
	write(t, dir, "b.txt", "new\n")

	snap, err = Read(context.Background(), dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if snap.Unstaged != 1 || snap.Untracked != 1 || snap.Staged != 0 {
		t.Fatalf("changes = %d staged / %d unstaged / %d untracked, want 0/1/1",
			snap.Staged, snap.Unstaged, snap.Untracked)
	}
	if snap.Clean() {
		t.Error("Clean() is true with changes on disk")
	}
}

// A linked worktree is a berth of the same project, and Read says which one it
// was pointed at.
func TestReadSeesLinkedWorktrees(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	write(t, dir, "a.txt", "hi\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")

	wt := filepath.Join(t.TempDir(), "berth-a")
	run(t, dir, "worktree", "add", "-q", wt, "-b", "feat")

	snap, err := Read(context.Background(), wt)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if snap.Branch != "feat" {
		t.Errorf("branch in the worktree = %q, want feat", snap.Branch)
	}
	if len(snap.Worktrees) != 2 {
		t.Fatalf("worktrees = %+v, want 2", snap.Worktrees)
	}
	var current string
	for _, w := range snap.Worktrees {
		if w.Current {
			current = w.Path
		}
	}
	if groups.Canonical(current) != groups.Canonical(wt) {
		t.Fatalf("current worktree = %q, want %q", current, wt)
	}
}

func TestReadDetachedHead(t *testing.T) {
	requireGit(t)
	dir := newRepo(t)
	write(t, dir, "a.txt", "hi\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")
	run(t, dir, "checkout", "-q", "--detach")

	snap, err := Read(context.Background(), dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !snap.Detached || snap.Branch != "" {
		t.Fatalf("branch = %q detached = %v, want detached", snap.Branch, snap.Detached)
	}
	if snap.Head == "" {
		t.Error("head is empty on a detached checkout")
	}
}

// A directory outside any checkout is a different answer from a failure.
func TestReadOutsideARepository(t *testing.T) {
	_, err := Read(context.Background(), t.TempDir())
	if err != ErrNotARepository {
		t.Fatalf("err = %v, want ErrNotARepository", err)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	run(t, dir, "init", "-q")
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "Test")
	return dir
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	// -c commit.gpgsign=false: a developer's global config must not decide
	// whether this test can make a commit.
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
