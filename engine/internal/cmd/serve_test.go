package cmd

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// TestAlreadyRunningErrorFollowsTheSocket pins the blocked-start decision:
// `serve --detach` refuses only while another process really answers on this
// socket, so a stale socket file does not block a legitimate restart.
func TestAlreadyRunningErrorFollowsTheSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the daemon socket harness is unix-only")
	}
	socket := filepath.Join(t.TempDir(), "d.sock")
	if err := alreadyRunningError(socket); err != nil {
		t.Fatalf("a socket nobody listens on must not block the start: %v", err)
	}

	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	err = alreadyRunningError(socket)
	if err == nil || !strings.Contains(err.Error(), "daemon already running") {
		t.Fatalf("err = %v, want the already-running refusal", err)
	}
}

// TestOpenDaemonLogCreatesTheLogOwnerOnly: the detached child writes its only
// pre-logger diagnostics here, so the file has to exist before the fork and
// must not be readable by other users.
func TestOpenDaemonLogCreatesTheLogOwnerOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)

	f, err := openDaemonLog()
	if err != nil {
		t.Fatalf("openDaemonLog: %v", err)
	}
	path := f.Name()
	defer f.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the log file was not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("log mode = %o, want no group or other access", perm)
	}
}
