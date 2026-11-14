package store

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func requireLocalFilesystem(path string) error {
	var info unix.Statfs_t
	if err := unix.Statfs(path, &info); err != nil {
		return err
	}
	if !localDarwinStorage(unix.ByteSliceToString(info.Fstypename[:]), uint64(info.Flags)) {
		return fmt.Errorf("Runtime Service storage requires a local filesystem; network SQLite WAL storage is refused")
	}
	return nil
}
