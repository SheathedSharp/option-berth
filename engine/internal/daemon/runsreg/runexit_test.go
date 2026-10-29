package runsreg

import (
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
)

// A recorded exit is announced once. The workplace record is what listens: it
// is how a run the CLI did not wait for gets an outcome, since by the time the
// run ends the CLI that asked for it is gone.
//
// The hook registry is process-wide and has no unregister, so a test hook must
// filter on the run it cares about and must never block: every later test's
// exits pass through it too, and a listener that fills a channel would stall
// the registry's own exit path.

func exitsFor(runID string) <-chan daemon.RunExit {
	got := make(chan daemon.RunExit, 4)
	daemon.OnRunExit(func(e daemon.RunExit) {
		if e.RunID != runID {
			return
		}
		select {
		case got <- e:
		default:
		}
	})
	return got
}

func TestExitedRunAnnouncesTheExit(t *testing.T) {
	r := testRegistry(100)
	base := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	rec := Record{ID: "run-1", PID: 100, Group: "demo", Name: "api", StartedAt: base}
	r.Register(rec)
	got := exitsFor("run-1")

	if _, ok := r.ExitedRun(rec, 3, false); !ok {
		t.Fatal("ExitedRun did not record the exit")
	}

	select {
	case e := <-got:
		if e.PID != 100 || e.Code != 3 {
			t.Fatalf("announced exit = %+v", e)
		}
		if e.Reason != ReasonCrashed {
			t.Fatalf("reason = %q, want %q", e.Reason, ReasonCrashed)
		}
		if e.ExitedAt.IsZero() {
			t.Fatal("announced exit has no time")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recording an exit announced nothing")
	}
}

func TestStartFailedAnnouncesTheExit(t *testing.T) {
	r := testRegistry()
	base := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	rec := Record{ID: "run-2", PID: 101, Group: "demo", Name: "api", StartedAt: base}
	got := exitsFor("run-2")

	r.StartFailed(rec, "exec: no such file")

	select {
	case e := <-got:
		// A run that never started has ended too, and the block that recorded
		// it must not stay `running`.
		if e.Reason != ReasonStartFailed || e.Code == 0 {
			t.Fatalf("announced start failure = %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a start failure announced nothing")
	}
}

// TestAnExitIsAnnouncedOutsideTheRegistryLocks pins the ordering that keeps a
// listener from running on the registry's critical path: a hook that takes the
// mirror lock must not deadlock the exit that announced it.
func TestAnExitIsAnnouncedOutsideTheRegistryLocks(t *testing.T) {
	r := testRegistry(200)
	base := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	rec := Record{ID: "run-3", PID: 200, Group: "demo", Name: "api", StartedAt: base}
	r.Register(rec)

	done := make(chan struct{})
	daemon.OnRunExit(func(e daemon.RunExit) {
		if e.RunID != "run-3" {
			return
		}
		// What a listener that records something does: take a lock of its own
		// that the registry also takes.
		r.Prune()
		close(done)
	})

	finished := make(chan struct{})
	go func() {
		r.ExitedRun(rec, 0, false)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("recording an exit deadlocked against its own listener")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the listener never ran")
	}
}
