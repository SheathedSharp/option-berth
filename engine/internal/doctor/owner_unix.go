//go:build !windows

package doctor

import (
	"os"
	"syscall"
)

// fileOwner reports the numeric uid that owns a file, and false when the
// Stat_t behind it is not the shape this platform uses.
func fileOwner(info os.FileInfo) (int, bool) {
	stat, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, false
	}
	return int(stat.Uid), true
}
