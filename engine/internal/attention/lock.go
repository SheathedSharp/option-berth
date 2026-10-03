//go:build !windows

package attention

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// withArtifactLock serializes attention writers across CLI/adapter processes.
// Atomic rename protects readers from partial JSON; this sidecar flock protects
// the read-modify-write steps that preserve assessments and implement CAS. The
// kernel releases it when a process exits, so no stale-file timeout can allow
// two writers into the critical section.
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
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("attention: acquire lock: %w", err)
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return fn()
}
