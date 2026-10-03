package runsreg

import (
	"fmt"
	"time"

	"github.com/sheathedsharp/option-berth/internal/runs"
)

// ImportLegacy transfers the disk registry only after its file transaction
// succeeds. Failure leaves memory unchanged and is fatal to daemon startup:
// an empty registry is not an adequate substitute for unobserved live runs.
func (r *Registry) ImportLegacy() (int, error) {
	return r.importLegacy(runs.ConsumeLegacy)
}

// consume is the transaction seam, not a mutable global. Production always
// uses ConsumeLegacy; tests inject a failure at its real commit boundary.
func (r *Registry) importLegacy(consume func(func([]runs.Entry) ([]runs.Entry, error)) error) (int, error) {
	r.mirrorMu.Lock()
	defer r.mirrorMu.Unlock()

	var pending []Record
	err := consume(func(entries []runs.Entry) ([]runs.Entry, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		// Existing memory wins on a retry. In particular, importing an old
		// same-PID entry must not replace a newer live registration.
		pending = make([]Record, 0, len(entries))
		for _, e := range entries {
			if _, exists := r.runs[e.PID]; exists {
				continue
			}
			rec := Record{
				ID: e.ID, PID: e.PID, PPID: e.PPID,
				Group: e.GroupOf(), Name: e.NameOf(), Cmd: e.Cmd,
				Cwd: e.Cwd, PortHint: e.PortHint, SpecHash: e.SpecHash,
			}
			if t, err := time.Parse(time.RFC3339Nano, e.StartedAt); err == nil {
				rec.StartedAt = t
			}
			pending = append(pending, rec)
		}
		if !r.Mirror {
			return nil, nil
		}
		replacement := make([]runs.Entry, 0, len(r.runs)+len(pending))
		for _, rec := range r.runs {
			replacement = append(replacement, mirrorEntry(rec))
		}
		for _, rec := range pending {
			replacement = append(replacement, mirrorEntry(rec))
		}
		return replacement, nil
	})
	if err != nil {
		return 0, fmt.Errorf("importing runs.json: %w", err)
	}
	r.mu.Lock()
	for _, rec := range pending {
		r.runs[rec.PID] = rec
	}
	// Update only imported rows: stopping flags on pre-existing rows can
	// progress under mu while disk I/O runs, and must not be overwritten.
	r.mu.Unlock()
	return len(pending), nil
}
