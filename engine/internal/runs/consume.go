package runs

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// ConsumeLegacy executes one read-transform-replace transaction on runs.json.
// The callback runs while the sidecar lock is held and must not call functions
// in this package that acquire the same lock. This keeps legacy import and
// concurrent CLI writers in one ordered file transaction.
//
// Every recorded entry reaches the callback, including ones whose process is
// gone. Those dead entries are the runs the recovery transaction must turn into
// terminal facts; filtering them here would destroy the evidence before the one
// writer that can record it sees it. The callback decides what the live file
// keeps, and its replacement is written in the same transaction.
func ConsumeLegacy(transform func([]Entry) ([]Entry, error)) error {
	if transform == nil {
		return nil
	}
	return withLock(func() error {
		reg, err := loadForImport()
		if err != nil {
			return err
		}
		entries := reg.Active()
		// The map has no order. A stable input order makes a recovery
		// transaction reproducible and keeps its tests meaningful.
		sort.Slice(entries, func(i, j int) bool { return entries[i].PID < entries[j].PID })
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
