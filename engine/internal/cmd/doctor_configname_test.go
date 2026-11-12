package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

// TestRenameLegacyConfigPlain: outside git the repair is a plain rename.
func TestRenameLegacyConfigPlain(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, groups.LegacyConfigName)
	if err := os.WriteFile(legacy, []byte("name: catalog\n"), 0o644); err != nil {
		t.Fatalf("writing the legacy file: %v", err)
	}
	if _, err := renameLegacyConfig(root, groups.FilesIn(root)); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("%s is still there", legacy)
	}
	if data, err := os.ReadFile(filepath.Join(root, groups.ConfigName)); err != nil || string(data) != "name: catalog\n" {
		t.Errorf("%s = %q, %v", groups.ConfigName, data, err)
	}
}

// The repair changes the selected file, never the Git index.
func TestRenameLegacyConfigPreservesGitIndex(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	gitIn := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	gitIn("init", "-q")
	if err := os.WriteFile(filepath.Join(root, groups.LegacyConfigName), []byte("name: catalog\n"), 0o644); err != nil {
		t.Fatalf("writing the legacy file: %v", err)
	}
	gitIn("add", groups.LegacyConfigName)

	if _, err := renameLegacyConfig(root, groups.FilesIn(root)); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if tracked(root, groups.ConfigName) {
		t.Errorf("%s was unexpectedly staged by the fix", groups.ConfigName)
	}
	if !tracked(root, groups.LegacyConfigName) {
		t.Errorf("%s was unexpectedly removed from the Git index", groups.LegacyConfigName)
	}
}

// TestRenameLegacyConfigRefusesTwoFiles: with both names present the repair
// touches nothing, because one of the two would be lost.
func TestRenameLegacyConfigRefusesTwoFiles(t *testing.T) {
	root := t.TempDir()
	present := map[string]string{groups.ConfigName: "name: new\n", groups.LegacyConfigName: "name: old\n"}
	for name, body := range present {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	if _, err := renameLegacyConfig(root, groups.FilesIn(root)); err == nil {
		t.Fatal("renamed with two config files present")
	}
	if data, _ := os.ReadFile(filepath.Join(root, groups.ConfigName)); string(data) != "name: new\n" {
		t.Errorf("%s changed: %q", groups.ConfigName, data)
	}
}

func tracked(dir, name string) bool {
	return exec.Command("git", "-C", dir, "ls-files", "--error-unmatch", "--", name).Run() == nil
}
func TestRenameLegacyDoesNotOverwriteConcurrentTarget(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, groups.LegacyConfigName)
	if err := os.WriteFile(old, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	selected := groups.FilesIn(dir)
	target := filepath.Join(dir, groups.ConfigName)
	if err := os.WriteFile(target, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := renameLegacyConfig(dir, selected); err == nil {
		t.Fatal("overwrote a late-created target")
	}
	if data, _ := os.ReadFile(target); string(data) != "new" {
		t.Fatal("target modified")
	}
	if data, _ := os.ReadFile(old); string(data) != "old" {
		t.Fatal("source modified")
	}
}
