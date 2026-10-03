package runsreg

import (
	"testing"
	"time"
)

// clock is called after the log read and before locking the mutation. Use this
// existing dependency as a deterministic barrier against the legacy entry point.
func TestExitedDoesNotAdoptReplacementAfterIO(t *testing.T) {
	r, old := mirroredRegistry(t)
	next := old
	next.ID = "replacement"
	next.StartedAt = old.StartedAt.Add(time.Nanosecond)
	r.now = func() time.Time { r.Register(next); return next.StartedAt }
	if exit, ok := r.Exited(old.PID, 19, false); ok {
		t.Fatalf("old callback recorded an exit for %+v", exit)
	}
	requireMirroredRun(t, r, next)
	if exits := r.Exits(); len(exits) != 0 {
		t.Fatalf("old callback contaminated history: %+v", exits)
	}
}
