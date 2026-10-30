package cmd

import (
	"os"
	"testing"
)

// The daemon runs in option-berth's state directory, not in whatever checkout
// invoked it: a daemon started from a worktree must not keep that checkout as
// its working directory after the checkout is deleted.
func TestDaemonWorkDirIsTheStateDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)

	got, err := daemonWorkDir()
	if err != nil {
		t.Fatalf("daemonWorkDir: %v", err)
	}
	if got != home {
		t.Errorf("daemonWorkDir = %q, want the state directory %q", got, home)
	}
	info, err := os.Stat(got)
	if err != nil || !info.IsDir() {
		t.Fatalf("state directory %q is not usable: %v", got, err)
	}
}
