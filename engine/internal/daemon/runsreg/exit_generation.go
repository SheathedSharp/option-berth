package runsreg

// Exited snapshots the generation currently registered for a legacy PID-only
// notification. A caller that already knows the run (such as the child reaper)
// must use ExitedRun instead, so a late callback cannot pick a replacement.
func (r *Registry) Exited(pid, code int, stopped bool) (Exit, bool) {
	expected, ok := r.Lookup(pid)
	if !ok {
		return Exit{}, false
	}
	return r.ExitedRun(expected, code, stopped)
}

// ExitedRun records an exit only for the supplied generation. Log I/O happens
// outside both locks; identity is rechecked before any memory/disk mutation.
func (r *Registry) ExitedRun(expected Record, code int, stopped bool) (Exit, bool) {
	return r.exitedRun(expected, code, stopped, tailLines)
}

// The per-call reader makes the I/O boundary testable without a mutable global
// hook or a production callback under the registry lock.
func (r *Registry) exitedRun(expected Record, code int, stopped bool, read func(string, int64, int) []string) (Exit, bool) {
	rec, ok := r.Lookup(expected.PID)
	if !ok || !sameRun(rec, expected) {
		return Exit{}, false
	}
	var lines []string
	if rec.LogPath != "" {
		lines = read(rec.LogPath, rec.LogOffset, lastLineCount)
	}
	now := r.clock()
	r.mirrorMu.Lock()
	defer r.mirrorMu.Unlock()
	r.mu.Lock()
	rec, ok = r.runs[expected.PID]
	if !ok || !sameRun(rec, expected) {
		r.mu.Unlock()
		return Exit{}, false
	}
	delete(r.runs, expected.PID)
	// Use the current same-generation record: stopping/rename may have changed
	// during log I/O and must not be rolled back to the captured metadata.
	e := Exit{Record: rec, Code: code, Reason: occupiedExit(exitReasonForRecord(code, stopped, rec), lines),
		ExitedAt: now, LastLines: lines}
	r.exits = append(r.exits, e)
	if len(r.exits) > maxExits {
		r.exits = append([]Exit(nil), r.exits[len(r.exits)-maxExits:]...)
	}
	history := r.history
	r.mu.Unlock()
	r.mirrorRemoveLocked(rec)
	if history != nil {
		r.persistenceResult("exit.append", history.AppendRunExit(storeRow(e)))
	}
	return e, true
}
