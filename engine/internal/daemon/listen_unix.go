//go:build !windows

package daemon

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// listen binds the unix socket. The socket is always 0600 — local and
// same-user only, enforced by the filesystem (spec, "Transport details"). The
// umask is cleared around the bind: a restrictive umask cannot make a socket
// *more* permissive, but a permissive one would leave it group- and
// world-readable on some systems.
func listen(path string) (net.Listener, error) {
	if err := prepareSocketDir(filepath.Dir(path), OwnsSocketDir(path)); err != nil {
		return nil, err
	}

	previous := syscall.Umask(0o177)
	listener, err := net.Listen("unix", path)
	syscall.Umask(previous)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("tightening socket: %w", err)
	}
	return listener, nil
}

// dial connects to the unix socket.
func dial(path string) (net.Conn, error) {
	return net.Dial("unix", path)
}

// removeStaleSocket deletes a socket file a daemon that died without cleaning
// up left behind. It is only called once the lock has been acquired, so the
// file provably belongs to no live daemon.
func removeStaleSocket(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// prepareSocketDir makes sure dir exists before the socket is bound. A
// directory option-berth owns (the default location) is created 0700 and
// tightened to 0700 even when it was already there. A directory the user
// pointed BERTH_SOCKET at is created 0700 when missing, but an existing one is
// left exactly as it is: `BERTH_SOCKET=/tmp/x.sock` must neither fail on
// "chmod /tmp: operation not permitted" nor silently tighten /tmp. Either way
// the socket file itself is 0600, which is what keeps other users out.
func prepareSocketDir(dir string, ours bool) error {
	_, statErr := os.Stat(dir)
	existed := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return fmt.Errorf("checking socket directory %s: %w", dir, statErr)
	}
	mkErr := os.MkdirAll(dir, 0o700)
	if mkErr != nil {
		return fmt.Errorf("creating socket directory: %w", mkErr)
	}
	if existed && !ours {
		return nil
	}
	// MkdirAll applies the umask and does nothing to a directory we own that
	// already exists, so the mode is set explicitly.
	chmodErr := os.Chmod(dir, 0o700)
	if chmodErr != nil {
		return fmt.Errorf("tightening socket directory: %w", chmodErr)
	}
	return nil
}
