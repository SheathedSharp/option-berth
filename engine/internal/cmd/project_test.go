package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProjectAtUsesQualifiedLinkedWorktreeName(t *testing.T) {
	root := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(root, "init", "-q")
	run(root, "config", "user.name", "option-berth test")
	run(root, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(root, "add", "README")
	run(root, "commit", "-qm", "fixture")

	linked := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-feature")
	t.Cleanup(func() { _ = os.RemoveAll(linked) })
	run(root, "worktree", "add", "-q", "-b", "feature", linked)
	if err := os.WriteFile(filepath.Join(linked, "oberth.yaml"), []byte("name: custom-name\nservices: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	name, gotRoot := projectAt(linked)
	resolvedLinked, err := filepath.EvalSymlinks(linked)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Base(root) + "@" + filepath.Base(root) + "-feature"
	if name != want || gotRoot != resolvedLinked {
		t.Fatalf("projectAt = (%q, %q), want (%q, %q)", name, gotRoot, want, resolvedLinked)
	}
}
