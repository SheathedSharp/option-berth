//go:build darwin

package cmd

import "golang.org/x/sys/unix"

// ioctlReadTermios and ioctlWriteTermios are the ioctl numbers the terminal
// driver uses on this platform; Darwin's differ from Linux's.
const (
	ioctlReadTermios  = unix.TIOCGETA
	ioctlWriteTermios = unix.TIOCSETA
)
