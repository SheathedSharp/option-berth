package runs

import (
	"fmt"
	"time"
)

// RemoveIfMatch removes the generation the caller observed, not whichever run
// now occupies its PID. Comparison and deletion share one file transaction.
// Missing or different generations are no-ops and do not rewrite the file.
func RemoveIfMatch(expected Entry) (bool, error) {
	if expected.PID <= 0 {
		return false, fmt.Errorf("runs: invalid pid %d", expected.PID)
	}
	removed := false
	err := withLock(func() error {
		reg, err := loadForImport()
		if err != nil {
			return err
		}
		current, ok := reg.Runs[expected.PID]
		if !ok || !sameGeneration(current, expected) {
			return nil
		}
		delete(reg.Runs, expected.PID)
		if err := save(reg); err != nil {
			return err
		}
		removed = true
		return nil
	})
	return removed, err
}

func sameGeneration(a, b Entry) bool {
	if a.PID != b.PID || a.ID != b.ID {
		return false
	}
	at, ae := time.Parse(time.RFC3339Nano, a.StartedAt)
	bt, be := time.Parse(time.RFC3339Nano, b.StartedAt)
	if ae == nil && be == nil && !at.IsZero() && !bt.IsZero() {
		return at.Equal(bt)
	}
	// Legacy records can carry an ID without a usable time. With neither,
	// PID equality alone is not proof of a generation; keep unknown evidence.
	return a.ID != "" && a.StartedAt == b.StartedAt
}
