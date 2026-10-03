package runsreg

import (
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/internal/state"
)

func stopReceiptFixture(t *testing.T) (*Registry, Record, func(int)) {
	t.Helper()
	r := testRegistry(42)
	r.now = func() time.Time { return time.Unix(100, 0) }
	rec := r.Register(Record{PID: 42, ID: "first", Group: "example", Name: "worker", StartedAt: time.Unix(1, 0)})
	observe := r.StopSignalObserver([]state.Run{{RootPID: 42, ID: rec.ID}}, ReasonStopped)
	if observe == nil {
		t.Fatal("no observer for registered generation")
	}
	return r, rec, observe
}

func TestStopReceiptDoesNotHideLiveRun(t *testing.T) {
	r, rec, observe := stopReceiptFixture(t)
	before, _ := r.Lookup(rec.PID)
	if before.stopping || before.stoppingReason != "" {
		t.Fatal("preparation mutated facts")
	}
	observe(rec.PID)
	if len(r.List()) != 1 || !r.Live(rec.Group, rec.Name) || len(r.GroupRuns(rec.Group)) != 1 {
		t.Fatal("accepted signal was treated as exit")
	}
	if r.ServiceFacts()[servicefacts.Key{Group: rec.Group, Service: rec.Name}].Stopping {
		t.Fatal("service fact hid a still-live run")
	}
	e, ok := r.ExitedRun(rec, 7, false)
	if !ok || e.Reason != ReasonStopped {
		t.Fatalf("exit=%+v ok=%v", e, ok)
	}
}

func TestNoStopReceiptPreservesFailureAndCrashFacts(t *testing.T) {
	r, rec, observe := stopReceiptFixture(t)
	observe(999) // an unrelated unit cannot mark this run
	if len(r.List()) != 1 || !r.Live(rec.Group, rec.Name) {
		t.Fatal("failed stop hid run")
	}
	e, ok := r.ExitedRun(rec, 7, false)
	if !ok || e.Reason != ReasonCrashed {
		t.Fatalf("exit=%+v ok=%v", e, ok)
	}
}

func TestStopReceiptCannotChangeReplacementGeneration(t *testing.T) {
	for _, changedID := range []bool{false, true} {
		r, rec, observe := stopReceiptFixture(t)
		replacement := rec
		if changedID {
			replacement.ID = "replacement"
		} else {
			replacement.StartedAt = rec.StartedAt.Add(time.Nanosecond)
		}
		r.Register(replacement)
		observe(rec.PID)
		got, _ := r.Lookup(rec.PID)
		if got.stopping || got.stoppingReason != "" || !sameRun(got, replacement) {
			t.Fatalf("late receipt changed replacement: %+v", got)
		}
	}
}

func TestStopReceiptCorrectsExactExitAfterReap(t *testing.T) {
	r, rec, observe := stopReceiptFixture(t)
	old, ok := r.ExitedRun(rec, 7, false)
	if !ok || old.Reason != ReasonCrashed {
		t.Fatal("fixture has no crash")
	}
	newer := rec
	newer.ID = "replacement"
	newer.StartedAt = time.Unix(50, 0)
	r.Register(newer)
	if _, ok := r.ExitedRun(newer, 9, false); !ok {
		t.Fatal("fixture has no replacement exit")
	}
	observe(rec.PID)
	got, ok := r.ExitForRun(rec.ID, rec.PID, rec.StartedAt)
	if !ok || got.Reason != ReasonStopped {
		t.Fatalf("old exit=%+v", got)
	}
	got, ok = r.ExitForRun(newer.ID, newer.PID, newer.StartedAt)
	if !ok || got.Reason != ReasonCrashed {
		t.Fatalf("new exit=%+v", got)
	}
}

func TestStopReceiptRejectsConflictingSnapshotIdentities(t *testing.T) {
	r, rec, _ := stopReceiptFixture(t)
	for _, owners := range [][]state.Run{
		{{RootPID: 42, ID: "unrelated"}},
		{{RootPID: 42, ID: rec.ID}, {RootPID: 42, ID: "unrelated"}},
		{{RootPID: 42, ID: "unrelated"}, {RootPID: 42, ID: rec.ID}},
	} {
		if r.StopSignalObserver(owners, "") != nil {
			t.Fatal("conflicting identity was accepted")
		}
	}
	if r.StopSignalObserver([]state.Run{{RootPID: 42}}, "") == nil {
		t.Fatal("explicit PID lost current-generation capture")
	}
}

func TestStopReceiptDoesNotCorrectFutureOrStaleExit(t *testing.T) {
	for _, at := range []time.Time{time.Unix(99, 0), time.Unix(100, 0).Add(stopWindow)} {
		r, rec, observe := stopReceiptFixture(t)
		r.ExitedRun(rec, 7, false)
		r.now = func() time.Time { return at }
		observe(rec.PID)
		if r.Exits()[0].Reason != ReasonCrashed {
			t.Fatal("unrelated time window was corrected")
		}
	}
}

func TestStopReceiptDoesNotClearAnotherStopOrLegacyMarker(t *testing.T) {
	r, rec, first := stopReceiptFixture(t)
	second := r.StopSignalObserver([]state.Run{{RootPID: rec.PID, ID: rec.ID}}, ReasonReadyTimeout)
	first(rec.PID)
	second(rec.PID)
	got, _ := r.Lookup(rec.PID)
	if got.stopping || got.stoppingReason != ReasonReadyTimeout {
		t.Fatalf("facts=%+v", got)
	}
	r.Stopping([]int{rec.PID}) // explicit legacy consumer remains compatible
	first(rec.PID)
	got, _ = r.Lookup(rec.PID)
	if !got.stopping {
		t.Fatal("receipt undid an independent legacy marker")
	}
}
