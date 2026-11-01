//go:build linux

package cmd

import "golang.org/x/sys/unix"

// ioctlReadTermios and ioctlWriteTermios are the ioctl numbers the terminal
// driver uses on this platform. They differ between Linux and the BSDs, which
// is why each platform names its own pair rather than sharing one constant.
const (
	ioctlReadTermios  = unix.TCGETS
	ioctlWriteTermios = unix.TCSETS
)
