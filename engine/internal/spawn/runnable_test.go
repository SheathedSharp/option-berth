package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The four answers a caller can act on differently: runnable, there but not
// runnable (the broken symlink this machine had seven of), absent, and a path
// the caller gave itself.
func TestCheckRunnable(t *testing.T) {
	dir := t.TempDir()
	env := []string{"PATH=" + dir}

	exec := filepath.Join(dir, "good")
	if err := os.WriteFile(exec, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "broken")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		argv0 string
		want  string // "" means no error
	}{
		{"runnable", "good", ""},
		{"installed but not executable", "plain", "cannot be run"},
		{"link with no target", "broken", "broken symlink"},
		{"not installed", "nope", "not on PATH"},
		{"explicit path", exec, ""},
		{"explicit path that is gone", filepath.Join(dir, "missing"), "cannot be run"},
		{"no command at all", "", "no command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckRunnable(tc.argv0, env)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("CheckRunnable(%q) = %v, want nil", tc.argv0, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CheckRunnable(%q) = %v, want it to mention %q", tc.argv0, err, tc.want)
			}
		})
	}
}

// A service inherits the daemon's environment, so the PATH that decides is the
// one passed in — not this process's.
func TestCheckRunnableUsesTheGivenEnvironment(t *testing.T) {
	dir := t.TempDir()
	name := "only-in-env"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := CheckRunnable(name, []string{"PATH=" + dir}); err != nil {
		t.Fatalf("a command on the given PATH was not found: %v", err)
	}
	if err := CheckRunnable(name, []string{"PATH=/nowhere"}); err == nil {
		t.Fatal("a command missing from the given PATH was accepted")
	}
}

func TestCheckRunnableInDirResolvesRelativeWrapper(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mvnw")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckRunnableInDir("./mvnw", dir, []string{"PATH=/nowhere"}); err != nil {
		t.Fatalf("relative wrapper was checked against the daemon cwd: %v", err)
	}
}
