//go:build !windows

package runtimesvc

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// permOf reports the permission bits at path and fails the test if it cannot
// be stat'ed.
func permOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

// TestPrepareSocketDirLeavesAUserDirectoryAlone is the smoke-test regression:
// `BERTH_SOCKET=/tmp/x.sock` used to fail with "chmod /tmp: operation not
// permitted" — and, pointed at a directory it did not own, it would quietly
// tighten that directory to 0700.
func TestPrepareSocketDirLeavesAUserDirectoryAlone(t *testing.T) {
	userDir := t.TempDir()
	if err := os.Chmod(userDir, 0o755); err != nil {
		t.Fatalf("setting up a 0755 directory: %v", err)
	}

	if err := prepareSocketDir(userDir, false); err != nil {
		t.Fatalf("prepareSocketDir on a user-supplied directory: %v", err)
	}

	if got := permOf(t, userDir); got != 0o755 {
		t.Errorf("a user-supplied directory was chmod'ed to %o, want it untouched at 0755", got)
	}
}

// TestPrepareSocketDirCreatesAMissingUserDirectory: a directory that does not
// exist yet is ours to make, so it is created 0700.
func TestPrepareSocketDirCreatesAMissingUserDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "sock")

	if err := prepareSocketDir(dir, false); err != nil {
		t.Fatalf("prepareSocketDir on a missing directory: %v", err)
	}

	if got := permOf(t, dir); got != 0o700 {
		t.Errorf("a directory we created has mode %o, want 0700", got)
	}
}

// TestPrepareSocketDirTightensOurOwnDirectory: the default location belongs to
// option-berth, so it is tightened even when it was already there and too loose.
func TestPrepareSocketDirTightensOurOwnDirectory(t *testing.T) {
	own := t.TempDir()
	if err := os.Chmod(own, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := prepareSocketDir(own, true); err != nil {
		t.Fatalf("prepareSocketDir on our own directory: %v", err)
	}

	if got := permOf(t, own); got != 0o700 {
		t.Errorf("our own socket directory has mode %o, want 0700", got)
	}
}

// TestListenOnAUserSuppliedSocketPath binds a bare socket in a directory the
// daemon does not own — the `BERTH_SOCKET=/tmp/x.sock` shape — and checks the
// socket file still ends up 0600.
func TestListenOnAUserSuppliedSocketPath(t *testing.T) {
	lnDir, err := os.MkdirTemp("", "oberth")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(lnDir) })
	if err := os.Chmod(lnDir, 0o755); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(lnDir, "d.sock")

	ln, err := listen(socket)
	if err != nil {
		t.Fatalf("listen on a user-supplied socket path: %v", err)
	}
	defer ln.Close()

	if got := permOf(t, socket); got != 0o600 {
		t.Errorf("socket mode = %o, want 0600", got)
	}
	if got := permOf(t, lnDir); got != 0o755 {
		t.Errorf("listen tightened the user's directory to %o, want 0755", got)
	}

	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatalf("dialling the socket we just bound: %v", err)
	}
	conn.Close()
}

// TestOwnsSocketDir separates the directory option-berth makes for itself from
// one the user pointed BERTH_SOCKET at.
func TestOwnsSocketDir(t *testing.T) {
	homeDir := func() (string, error) { return "/home/dev", nil }
	vars := map[string]string{"BERTH_SOCKET": "/tmp/x.sock"}
	getenv := func(key string) string { return vars[key] }

	if ownsSocketDir("/tmp/x.sock", getenv, homeDir, "linux") {
		t.Error("/tmp counted as a directory option-berth owns")
	}
	if !ownsSocketDir("/home/dev/.option-berth/daemon.sock", getenv, homeDir, "linux") {
		t.Error("the default config directory did not count as ours")
	}

	vars["XDG_RUNTIME_DIR"] = "/run/user/1000"
	if !ownsSocketDir("/run/user/1000/option-berth/daemon.sock", getenv, homeDir, "linux") {
		t.Error("the XDG runtime subdirectory did not count as ours")
	}
	if ownsSocketDir("/run/user/1000/daemon.sock", getenv, homeDir, "linux") {
		t.Error("the XDG runtime directory itself counted as ours")
	}
}
