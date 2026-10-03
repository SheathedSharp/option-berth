package runs

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// withLegacyLockUntil runs inside the kernel guard. Owner PID and age only
// decide whether a legacy file can be reclaimed; they are not mutual exclusion
// by themselves. The deadline is shared with guard acquisition.
func withLegacyLockUntil(deadline time.Time, fn func() error) error {
	lp := lockPath()
	// Owner-only, matching the daemon's paths.EnsureDir: this file may be the
	// first thing to create ~/.option-berth (a CLI `oberth run` before any
	// daemon), and the directory must not be listable by other users.
	if err := os.MkdirAll(parentDir(lp), 0o700); err != nil {
		return fmt.Errorf("runs: could not create config dir: %w", err)
	}

	const (
		maxWait     = 2 * time.Second
		staleAfter  = 5 * time.Second
		pollBackoff = 5 * time.Millisecond
	)

	for {
		f, err := os.OpenFile(lp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if _, writeErr := fmt.Fprintf(f, "%d\n", os.Getpid()); writeErr != nil {
				_ = f.Close()
				_ = os.Remove(lp)
				return fmt.Errorf("runs: write lock owner: %w", writeErr)
			}
			if closeErr := f.Close(); closeErr != nil {
				_ = os.Remove(lp)
				return fmt.Errorf("runs: close lock: %w", closeErr)
			}
			defer releaseLock(lp)
			return fn()
		}
		if !os.IsExist(err) {
			return fmt.Errorf("runs: acquire lock: %w", err)
		}
		// Lock held by someone else. Break a stale lock left by a crash.
		if info, statErr := os.Stat(lp); statErr == nil {
			if time.Since(info.ModTime()) > staleAfter && !lockOwnerAlive(lp) {
				if err := os.Remove(lp); err == nil || os.IsNotExist(err) {
					continue
				}
			}
		}
		if time.Now().After(deadline) {
			// Never steal a lock that may still be held by a live writer. A
			// crashed owner is reclaimed only after staleAfter and an owner
			// liveness check above.
			return fmt.Errorf("runs: lock busy after %s", maxWait)
		}
		time.Sleep(pollBackoff)
	}
}

func parentDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return "."
}

func releaseLock(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	owner, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err == nil && owner == os.Getpid() {
		_ = os.Remove(path)
	}
}

func lockOwnerAlive(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	owner, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || owner <= 0 {
		return false
	}
	return pidAlive(owner)
}
