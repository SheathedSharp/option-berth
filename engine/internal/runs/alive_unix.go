//go:build !windows

package runs

import "syscall"

// pidAlive reports whether a process currently exists. Signal 0 performs
// permission and existence checks without delivering anything: nil means the
// process is there, EPERM means it is there but owned by someone else, and
// ESRCH means it is gone.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	switch err := syscall.Kill(pid, 0); err {
	case nil, syscall.EPERM:
		return true
	default:
		return false
	}
}
