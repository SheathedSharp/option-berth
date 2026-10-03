// Package daemon implements `oberth serve`: the socket listener, the JSON-RPC
// dispatcher, the subscription fan-out and the process lifecycle. Handlers for
// namespaces owned by other packages register themselves with RegisterHandler
// from their own init(), so this package never imports them (contract §8).
package daemon

import (
	"net"

	"github.com/sheathedsharp/option-berth/internal/paths"
)

// WindowsPipe is the named pipe the daemon listens on under Windows
// (contract §7).
const WindowsPipe = paths.WindowsPipe

// SocketPath resolves the address the daemon listens on and every client
// dials. The resolution order lives in internal/paths, which owns the whole
// layout — this is only the daemon's name for it.
func SocketPath() string { return paths.Socket() }

// socketPathFrom is SocketPath with its environment injected, so the resolution
// order can be tested across the whole env matrix on one host.
func socketPathFrom(getenv func(string) string, home func() (string, error), goos string) string {
	return paths.SocketFrom(getenv, home, goos)
}

// defaultSocketPath is where the daemon would listen with BERTH_SOCKET unset:
// the directory option-berth creates and owns.
func defaultSocketPath() string { return paths.SocketWithoutEnv() }

// OwnsSocketDir reports whether the directory holding path is one option-berth makes
// for itself, rather than a directory the user pointed BERTH_SOCKET at.
// Only a directory we own may be tightened to 0700: `BERTH_SOCKET=/tmp/x.sock`
// must not chmod /tmp (contract §21).
func OwnsSocketDir(path string) bool { return paths.OwnsSocketDir(path) }

// ownsSocketDir is OwnsSocketDir with its environment injected, so the rule can
// be tested across the whole env matrix on one host.
func ownsSocketDir(path string, getenv func(string) string, home func() (string, error), goos string) bool {
	return paths.OwnsSocketDirFrom(getenv, home, goos, path)
}

// ConfigDir is the directory option-berth owns (see internal/paths): where the
// config, the database, the log, the lock and everything else live.
func ConfigDir() string { return paths.Dir() }

// LogPath is the daemon's rotated log file.
func LogPath() string { return paths.LogPath() }

// LockPath is the single-instance lock. It sits beside the socket so that a
// socket on a per-boot tmpfs takes its lock with it.
func LockPath() string { return lockPathFor(SocketPath()) }

func lockPathFor(socket string) string { return paths.Lock(socket) }

// Dial connects to a listening daemon at path. It is the one place clients
// learn whether the address is a unix socket or a named pipe.
func Dial(path string) (net.Conn, error) { return dial(path) }

// Listen binds the daemon's socket at path with the platform's same-user-only
// permissions: a 0700 directory holding a 0600 socket on Unix, a pipe whose
// DACL grants only the current user and SYSTEM on Windows.
func Listen(path string) (net.Listener, error) { return listen(path) }

// SocketAlive reports whether something is accepting connections at path.
func SocketAlive(path string) bool {
	c, err := dial(path)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
