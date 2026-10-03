package runsreg

import (
	"time"

	"github.com/sheathedsharp/option-berth/internal/runs"
)

// sameRun compares generation identity, never mutable labels or stop state.
func sameRun(a, b Record) bool {
	return a.PID == b.PID && a.ID == b.ID && a.StartedAt.Equal(b.StartedAt)
}

// mirrorEntry retains timestamp precision in the existing RFC3339 field.
// Rounding here would collapse two registrations within the same second;
// legacy readers using time.Parse(time.RFC3339, ...) accept the fractional part.
func mirrorEntry(rec Record) runs.Entry {
	return runs.Entry{
		PID: rec.PID, Tag: rec.Group, ID: rec.ID, Cmd: rec.Cmd,
		StartedAt: rec.StartedAt.Format(time.RFC3339Nano),
		Group:     rec.Group, Name: rec.Name, Cwd: rec.Cwd, PPID: rec.PPID,
		PortHint: rec.PortHint, SpecHash: rec.SpecHash,
	}
}

// mirrorAdd is the startup import's entry point after taking its snapshot.
// Normal mutations hold mirrorMu before changing memory and use the locked
// helpers below, so a replacement cannot be unregistered before its preceding
// disk write finishes. No second registry or asynchronous queue is needed.
func (r *Registry) mirrorAdd(rec Record) {
	r.mirrorMu.Lock()
	defer r.mirrorMu.Unlock()
	r.mirrorAddLocked(rec)
}

// mirrorAddLocked requires mirrorMu, but never holds mu during file I/O.
// Reject stale generations and read current labels instead of replaying a
// pre-rename record. This check also guards delayed import projections.
func (r *Registry) mirrorAddLocked(rec Record) {
	if !r.Mirror {
		return
	}
	current, ok := r.Lookup(rec.PID)
	if !ok || !sameRun(current, rec) {
		return
	}
	r.persistenceResult("mirror.add", runs.Add(mirrorEntry(current)))
}

// mirrorRemoveLocked requires mirrorMu and carries the removed generation
// all the way to the file lock. This also preserves a replacement written by
// another process, which does not share mirrorMu. Mirror failures retain the
// existing best-effort policy.
func (r *Registry) mirrorRemoveLocked(rec Record) {
	if !r.Mirror {
		return
	}
	_, err := runs.RemoveIfMatch(mirrorEntry(rec))
	r.persistenceResult("mirror.remove", err)
}
