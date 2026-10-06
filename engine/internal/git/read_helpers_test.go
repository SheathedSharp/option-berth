package git

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A read-only UI must not launch a repository's fsmonitor program. Such hooks
// can start long-lived children even after Git has returned a complete answer.
func TestReadsDoNotInvokeFSMonitorOrRewriteRepository(t *testing.T) {
	requireGit(t)
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fsmonitor hook fixture")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := newRepo(t)
	write(t, dir, "a.txt", "before\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "fixture")
	linked := filepath.Join(t.TempDir(), "linked")
	run(t, dir, "worktree", "add", "-q", "-b", "other", linked)
	hook := filepath.Join(dir, ".git", "fsmonitor-fixture")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf invoked >> \"$0.called\"\nprintf 'fixture-token\\000/\\000'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "config", "core.fsmonitor", hook)
	run(t, dir, "config", "core.fsmonitorHookVersion", "2")
	// Reading a commit summary must not invoke signature verification helpers.
	run(t, dir, "config", "log.showSignature", "true")
	before, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{dir, linked} {
		write(t, root, "a.txt", "after\n")
		write(t, root, "new.txt", "untracked\n")
		checks := []struct {
			name string
			read func() error
		}{
			{"overview", func() error {
				s, e := Read(context.Background(), root)
				if e == nil && (s.Unstaged != 1 || s.Untracked != 1) {
					t.Errorf("wrong counts: %+v", s)
				}
				return e
			}},
			{"files", func() error {
				s, e := ReadTree(context.Background(), root)
				if e == nil && len(s.Files) != 2 {
					t.Errorf("wrong files: %+v", s.Files)
				}
				return e
			}},
			{"diff", func() error {
				s, e := Diff(context.Background(), root, "a.txt")
				if e == nil && len(s.Files) != 1 {
					t.Errorf("wrong diff: %+v", s)
				}
				return e
			}},
		}
		for _, check := range checks {
			t.Run(filepath.Base(root)+"/"+check.name, func(t *testing.T) {
				if err := check.read(); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(hook + ".called"); !os.IsNotExist(err) {
					t.Fatal("read invoked configured fsmonitor helper")
				}
			})
		}
	}
	after, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read changed repository config")
	}
}
