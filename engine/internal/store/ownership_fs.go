package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// Resolve existing aliases before checking the filesystem. SQLite WAL cannot
// provide this ownership contract on a network filesystem.
// ValidateStorageFilesystem performs metadata-only, fail-closed validation.
func ValidateStorageFilesystem(path string) error {
	current, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return requireLocalFilesystem(resolved)
		}
		if !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("cannot resolve local storage filesystem for %s", path)
		}
		current = parent
	}
}

// Unknown/remote filesystems fail closed; callers only collect OS metadata.
func localStorageFilesystem(platform string, kind uint64) bool {
	switch platform {
	case "darwin":
		return kind&0x1000 != 0 // MNT_LOCAL
	case "linux":
		switch kind {
		case 0xef53, 0x58465342, 0x9123683e, 0x01021994, 0x858458f6, 0x794c7630, 0x2fc12fc1, 0xf2f52010:
			return true
		}
	case "windows":
		return kind == 2 || kind == 3 || kind == 6 // removable, fixed, RAM
	}
	return false
}

func localDarwinStorage(name string, flags uint64) bool {
	if !localStorageFilesystem("darwin", flags) {
		return false
	}
	return name == "apfs" || name == "hfs" || name == "tmpfs"
}
