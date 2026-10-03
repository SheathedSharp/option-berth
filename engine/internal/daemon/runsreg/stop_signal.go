package runsreg

import "github.com/sheathedsharp/option-berth/internal/state"

// StopSignalObserver captures exact generations before control starts, but
// changes no facts until an OS adapter confirms an accepted signal. The maps
// are immutable identities scoped to this call, not a second live registry.
func (r *Registry) StopSignalObserver(owners []state.Run, reason string) func(int) {
	if reason == "" {
		reason = ReasonStopped
	}
	expected := make(map[int]Record, len(owners))
	rejected := make(map[int]bool)
	r.mu.Lock()
	for _, owner := range owners {
		if owner.RootPID <= 0 || rejected[owner.RootPID] {
			continue
		}
		rec, ok := r.runs[owner.RootPID]
		if !ok || (owner.ID != "" && owner.ID != rec.ID) {
			delete(expected, owner.RootPID)
			rejected[owner.RootPID] = true
			continue
		}
		// Labels can change during an operation; retain identity only.
		expected[rec.PID] = Record{PID: rec.PID, ID: rec.ID, StartedAt: rec.StartedAt}
	}
	r.mu.Unlock()
	if len(expected) == 0 {
		return nil
	}
	return func(pid int) {
		if rec, ok := expected[pid]; ok {
			r.recordStopSignal(rec, reason)
		}
	}
}

// An accepted signal records an exit cause, not an exit or a hidden live run.
// If reap wins the race, correct only the matching recent exit. Concurrent
// operations never clear each other's flags and a late receipt cannot mutate
// a replacement run. Projection order remains mirrorMu -> mu -> disk.
func (r *Registry) recordStopSignal(expected Record, reason string) {
	r.mirrorMu.Lock()
	defer r.mirrorMu.Unlock()
	r.mu.Lock()
	if rec, ok := r.runs[expected.PID]; ok && sameRun(rec, expected) {
		rec.stoppingReason = reason
		// Deliberately leave stopping alone: live visibility comes from
		// observation, never a signal request. Legacy markers are not undone.
		r.runs[rec.PID] = rec
		r.mu.Unlock()
		return
	}
	now := r.clock()
	for i := len(r.exits) - 1; i >= 0; i-- {
		e := &r.exits[i]
		if !sameRun(e.Record, expected) {
			continue
		}
		if e.ExitedAt.Before(e.StartedAt) || e.ExitedAt.After(now) || now.Sub(e.ExitedAt) >= stopWindow || e.Reason == reason {
			r.mu.Unlock()
			return
		}
		e.Reason = reason
		copy, history := *e, r.history
		r.mu.Unlock()
		if history != nil {
			r.persistenceResult("exit.correct", history.CorrectRunExit(storeRow(copy)))
		}
		return
	}
	r.mu.Unlock()
}
