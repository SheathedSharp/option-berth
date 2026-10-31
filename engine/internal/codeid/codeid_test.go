package codeid

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=codeid-test", "GIT_AUTHOR_EMAIL=codeid@example.invalid",
		"GIT_COMMITTER_NAME=codeid-test", "GIT_COMMITTER_EMAIL=codeid@example.invalid",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.name", "codeid-test")
	gitRun(t, dir, "config", "user.email", "codeid@example.invalid")
	return dir
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveSourcesDistinguishesDefaultFromDisabled(t *testing.T) {
	if got, enabled := ResolveSources(nil); !enabled || !reflect.DeepEqual(got, []string{"."}) {
		t.Fatalf("nil = (%v, %v), want ([.] and enabled)", got, enabled)
	}
	if got, enabled := ResolveSources([]string{}); enabled || got != nil {
		t.Fatalf("empty = (%v, %v), want (nil, disabled)", got, enabled)
	}
	original := []string{"src", "go.mod"}
	got, enabled := ResolveSources(original)
	if !enabled || !reflect.DeepEqual(got, original) {
		t.Fatalf("list = (%v, %v)", got, enabled)
	}
	got[0] = "changed"
	if original[0] != "src" {
		t.Fatal("ResolveSources aliased the caller's slice")
	}
}

func TestTakeUsesGitTrackedAndUntrackedNotIgnored(t *testing.T) {
	requireGit(t)
	root := newRepo(t)
	write(t, root, ".gitignore", "ignored/\nnode_modules/\n")
	write(t, root, "src/a.go", "package a\n")
	write(t, root, "README.md", "readme\n")
	write(t, root, "ignored/secret.txt", "secret\n")
	write(t, root, "node_modules/x.js", "module\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-qm", "first")
	// An untracked, not-ignored file is source too.
	write(t, root, "src/b.go", "package a\n")

	snap, err := Take(context.Background(), root, []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Head == "" {
		t.Fatal("HEAD is empty in a repository with a commit")
	}
	for _, want := range []string{".gitignore", "src/a.go", "src/b.go", "README.md"} {
		if _, ok := snap.Files[want]; !ok {
			t.Errorf("%s missing from %v", want, snap.Files)
		}
	}
	for _, unwanted := range []string{"ignored/secret.txt", "node_modules/x.js"} {
		if _, ok := snap.Files[unwanted]; ok {
			t.Errorf("%s should be ignored by Git", unwanted)
		}
	}
	if snap.FileCount != len(snap.Files) {
		t.Fatalf("file_count=%d files=%d", snap.FileCount, len(snap.Files))
	}
}

// A service cwd is often a subdirectory of the repository, and sources may
// point at a sibling inside the same checkout. Both have to stay visible.
func TestTakeFromAServiceSubdirectory(t *testing.T) {
	requireGit(t)
	repo := newRepo(t)
	write(t, repo, "services/shared/schema.sql", "create table t;\n")
	write(t, repo, "services/api/app.py", "print('api')\n")
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "-qm", "first")

	root := filepath.Join(repo, "services", "api")
	snap, err := Take(context.Background(), root, []string{".", "../shared"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"app.py", "../shared/schema.sql"} {
		if _, ok := snap.Files[want]; !ok {
			t.Errorf("%s missing from %v", want, snap.Files)
		}
	}
}

func TestCompareCountsAppearedChangedAndGone(t *testing.T) {
	requireGit(t)
	root := newRepo(t)
	write(t, root, ".gitignore", "ignored/\n")
	write(t, root, "src/a.go", "package a\n")
	write(t, root, "README.md", "readme\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-qm", "first")

	start, err := Take(context.Background(), root, []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "src/a.go", "package a // changed\n")
	write(t, root, "src/c.go", "package a\n")
	if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	}
	// Ignored files are outside the snapshot and cannot change the answer.
	write(t, root, "ignored/noise.txt", "noise\n")

	current, err := Take(context.Background(), root, []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	changed, files := Compare(start, current)
	if !changed {
		t.Fatal("Compare reported unchanged after editing, adding and deleting source")
	}
	want := []string{"README.md", "src/a.go", "src/c.go"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("changed files = %v, want %v", files, want)
	}
}

func TestTakeWalkFallbackSkipsBuildTreesButHonoursAnExplicitSource(t *testing.T) {
	root := t.TempDir()
	write(t, root, "app.py", "print('app')\n")
	write(t, root, "node_modules/x.js", "module\n")
	write(t, root, "build/out.bin", "artifact\n")

	snap, err := Take(context.Background(), root, []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap.Files["app.py"]; !ok {
		t.Fatalf("app.py missing from %v", snap.Files)
	}
	if _, ok := snap.Files["node_modules/x.js"]; ok {
		t.Fatal("node_modules entered the non-Git walk")
	}
	if _, ok := snap.Files["build/out.bin"]; ok {
		t.Fatal("build/ entered the non-Git walk")
	}
	if snap.Head != "" {
		t.Fatalf("HEAD = %q outside a repository", snap.Head)
	}

	// The ignore list applies below a source, not to a directory the author
	// named explicitly.
	explicit, err := Take(context.Background(), root, []string{"build"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := explicit.Files["build/out.bin"]; !ok {
		t.Fatalf("explicit build source = %v", explicit.Files)
	}
}

func TestTakeMissingSourceIsEmptyRatherThanAnError(t *testing.T) {
	root := t.TempDir()
	snap, err := Take(context.Background(), root, []string{"gone"})
	if err != nil {
		t.Fatal(err)
	}
	if snap.FileCount != 0 || len(snap.Files) != 0 {
		t.Fatalf("missing source = %+v", snap)
	}
}

func TestSnapshotRoundTripAndPrune(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	root := t.TempDir()
	write(t, root, "app.py", "print('app')\n")
	snap, err := Take(context.Background(), root, []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	if err := Save("run-1", snap); err != nil {
		t.Fatal(err)
	}
	if err := Save("run-2", snap); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RunID != "run-1" || loaded.Digest != snap.Digest || !reflect.DeepEqual(loaded.Files, snap.Files) {
		t.Fatalf("loaded = %+v, want run-1 with the same digest and files", loaded)
	}
	if !strings.HasPrefix(Path("run-1"), filepath.Join(os.Getenv("BERTH_HOME"), "code")) {
		t.Fatalf("snapshot path %q is not under BERTH_HOME/code", Path("run-1"))
	}

	if err := PruneExcept(map[string]bool{"run-1": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("run-2"); !os.IsNotExist(err) {
		t.Fatalf("run-2 snapshot survived pruning: %v", err)
	}
	if _, err := Load("run-1"); err != nil {
		t.Fatalf("run-1 snapshot was pruned: %v", err)
	}
}

func TestLoadRejectsAnotherRunsSnapshot(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	root := t.TempDir()
	write(t, root, "app.py", "print('app')\n")
	snap, err := Take(context.Background(), root, []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	if err := Save("run-1", snap); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("run-2"); err == nil {
		t.Fatal("Load(run-2) accepted run-1's snapshot")
	}
}
