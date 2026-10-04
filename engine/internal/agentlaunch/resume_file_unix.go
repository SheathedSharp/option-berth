//go:build !windows

package agentlaunch

import (
	"golang.org/x/sys/unix"
	"os"
)

func openSessionFile(path string) (*os.File, error) {
	// Never block on a replaced FIFO and never follow a last-component symlink
	// after canonicalization. fstat in the caller rejects devices/directories.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
