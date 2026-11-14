package runsreg

import (
	"fmt"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runs"
)

// ImportLegacy transfers the disk registry only after its file transaction
// succeeds. Failure leaves memory unchanged and is fatal to runtime service startup:
// an empty registry is not an adequate substitute for unobserved live runs.
func (r *Registry) ImportLegacy() (int, error) {
	return r.Restore(runs.ConsumeLegacy)
}

// consume is the transaction seam, not a mutable global. Production always
// uses ConsumeLegacy; tests inject a failure at its real commit boundary.
func (r *Registry) importLegacy(consume func(func([]runs.Entry) ([]runs.Entry, error)) error) (int, error) {
	return r.Restore(consume)
}

// Restore performs the Runtime Service's single live-ledger recovery transaction.
// Supplying the transaction separately permits compatibility readers without a
// second registry or writer. Process adapters remain injected on Registry.
func (r *Registry) Restore(consume func(func([]runs.Entry) ([]runs.Entry, error)) error) (int, error) {
	r.mirrorMu.Lock()
	defer r.mirrorMu.Unlock()

	var pending []Record
	err := consume(func(entries []runs.Entry) ([]runs.Entry, error) {
		r.mu.Lock()
		existing := make(map[int]bool, len(r.runs))
		for pid := range r.runs {
			existing[pid] = true
		}
		r.mu.Unlock()
		// Existing memory wins on a retry. In particular, importing an old
		// same-PID entry must not replace a newer live registration.
		pending = make([]Record, 0, len(entries))
		for _, e := range entries {
			if existing[e.PID] {
				continue
			}
			rec := Record{
				ID: e.ID, PID: e.PID, PPID: e.PPID,
				Group: e.GroupOf(), Name: e.NameOf(), Cmd: e.Cmd,
				Cwd: e.Cwd, PortHint: e.PortHint, SpecHash: e.SpecHash,
				LogPath: e.LogPath, LogOffset: e.LogOffset, Executable: e.Executable,
			}
			if t, err := time.Parse(time.RFC3339Nano, e.StartedAt); err == nil {
				rec.StartedAt = t
			}
			// A surviving numeric PID is not proof that the old run survived.
			// Positive reuse/death evidence is rejected before publication.
			// Unknown birth evidence stays unknown; control still verifies it.
			if (r.Alive != nil && !r.Alive(rec.PID)) || r.pidReused(rec.PID, rec) {
				continue
			}
			pending = append(pending, rec)
		}
		if !r.Mirror {
			return nil, nil
		}
		r.mu.Lock()
		defer r.mu.Unlock()
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
