package store

import (
	"fmt"
	"golang.org/x/sys/windows"
	"path/filepath"
)

func requireLocalFilesystem(path string) error {
	root := filepath.VolumeName(path) + `\`
	ptr, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return err
	}
	if localStorageFilesystem("windows", uint64(windows.GetDriveType(ptr))) {
		return nil
	}
	return fmt.Errorf("Runtime Service storage requires a local writable volume; network or unknown volumes are refused")
}
