package runs

import (
	"encoding/json"
	"fmt"
	"os"
)

// ConsumeLegacy executes one read-transform-replace transaction on runs.json.
// The callback runs while the sidecar lock is held and must not call functions
// in this package that acquire the same lock. This keeps legacy import and
// concurrent CLI writers in one ordered file transaction.
func ConsumeLegacy(transform func([]Entry) ([]Entry, error)) error {
	if transform == nil {
		return nil
	}
	return withLock(func() error {
		reg, err := loadForImport()
		if err != nil {
			return err
		}
		for pid := range reg.Runs {
			if !pidAlive(pid) {
				delete(reg.Runs, pid)
			}
		}
		entries := reg.Active()
		replacement, err := transform(entries)
		if err != nil {
			return err
		}
		if len(replacement) == 0 {
			if err := os.Remove(Path()); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		}
		next := &Registry{Runs: make(map[int]Entry, len(replacement))}
		for _, entry := range replacement {
			next.Runs[entry.PID] = entry
		}
		return save(next)
	})
}

// Recovery and every mutation must distinguish "no file yet" from unreadable
// or invalid evidence. The legacy name is retained for existing callers.
// The best-effort read API may show an empty view on errors; a destructive
// ownership transfer must preserve the original evidence and return the error.
func loadForImport() (*Registry, error) {
	data, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return &Registry{Runs: map[int]Entry{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("runs: reading import: %w", err)
	}
	var reg Registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("runs: decoding import: %w", err)
	}
	if reg.Runs == nil {
		return nil, fmt.Errorf("runs: import has no runs object")
	}
	for pid, entry := range reg.Runs {
		if pid <= 0 || entry.PID != pid {
			return nil, fmt.Errorf("runs: import key %d does not identify entry PID %d", pid, entry.PID)
		}
	}
	return &reg, nil
}
