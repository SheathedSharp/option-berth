//go:build windows

package attention

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func withArtifactLock(home string, fn func() error) error {
	if err := os.MkdirAll(filepath.Join(home, AttentionDir), 0o700); err != nil {
		return fmt.Errorf("attention: create lock directory: %w", err)
	}
	lockPath := filepath.Join(home, AttentionDir, ".lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("attention: open lock: %w", err)
	}
	defer file.Close()
	overlap := &windows.Overlapped{}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, overlap); err != nil {
		return fmt.Errorf("attention: acquire lock: %w", err)
	}
	defer windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlap)
	return fn()
}
