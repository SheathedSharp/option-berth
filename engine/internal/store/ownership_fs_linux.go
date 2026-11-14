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
	if localStorageFilesystem("linux", uint64(info.Type)) {
		return nil
	}
	return fmt.Errorf("Runtime Service storage requires a verified local filesystem; filesystem type %#x is refused", info.Type)
}
