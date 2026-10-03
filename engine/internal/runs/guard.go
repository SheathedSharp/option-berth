package runs

import (
	"errors"
	"fmt"
	"os"
	"time"
)

var errGuardBusy = errors.New("registry transaction guard is held")

// withLock serializes the entire legacy sidecar protocol, including stale
// owner observation and removal. Without this kernel-owned guard, two
// reclaimers could both observe a dead owner and the second could unlink the
// first one's replacement lock. The guard inode is permanent; never unlink it.
// Keeping the legacy sidecar also preserves coordination with non-stealing
// older writers. Pre-fix writers that forcibly steal live locks must be stopped
// during upgrades; no advisory protocol can constrain an uncooperative writer.
func withLock(fn func() error) error {
	const budget = 2 * time.Second
	deadline := time.Now().Add(budget)
	lp := lockPath()
	if err := os.MkdirAll(parentDir(lp), 0o700); err != nil {
		return fmt.Errorf("runs: create lock directory: %w", err)
	}
	guard, err := os.OpenFile(lp+".guard", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("runs: open transaction guard: %w", err)
	}
	defer guard.Close()
	for {
		err := tryGuard(guard)
		if err == nil {
			defer releaseGuard(guard)
			return withLegacyLockUntil(deadline, fn)
		}
		if !errors.Is(err, errGuardBusy) {
			return fmt.Errorf("runs: acquire transaction guard: %w", err)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("runs: lock busy after %s: %w", budget, err)
		}
		time.Sleep(min(5*time.Millisecond, remaining))
	}
}
