package runtimecheck

import (
	"errors"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/runsreg"
	"github.com/sheathedsharp/option-berth/engine/internal/runs"
	"testing"
	"time"
)

func TestRecoveryTransactionPreservesGenerationAndLogEvidence(t *testing.T) {
	started := time.Date(2026, 11, 14, 0, 0, 0, 123456789, time.UTC)
	r := runsreg.New()
	r.Mirror = false
	r.Alive = func(int) bool { return true }
	r.StartTime = func(pid int) (time.Time, bool) {
		if pid == 20 {
			return started.Add(time.Minute), true
		}
		return started, true
	}
	r.Register(runsreg.Record{ID: "new", PID: 10, Group: "a", Name: "worker", StartedAt: started})
	entries := []runs.Entry{{PID: 10, ID: "old", StartedAt: started.Format(time.RFC3339Nano)}, {PID: 20, ID: "recycled", StartedAt: started.Format(time.RFC3339Nano)}, {PID: 30, ID: "survived", StartedAt: started.Format(time.RFC3339Nano), Group: "b", Name: "worker", LogPath: "/synthetic/run.log", LogOffset: 987, PortHint: 7012, SpecHash: "captured"}}
	transaction := func(f func([]runs.Entry) ([]runs.Entry, error)) error { _, err := f(entries); return err }
	n, err := r.Restore(transaction)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if rec, _ := r.Lookup(10); rec.ID != "new" {
		t.Fatal("recovery replaced memory generation")
	}
	if _, ok := r.Lookup(20); ok {
		t.Fatal("recycled PID published as live run")
	}
	rec, ok := r.Lookup(30)
	if !ok || rec.ID != "survived" || !rec.StartedAt.Equal(started) || rec.LogOffset != 987 || rec.PortHint != 7012 || rec.LogPath != "/synthetic/run.log" {
		t.Fatalf("lost recovery evidence: %+v", rec)
	}
	if n, err := r.Restore(transaction); err != nil || n != 0 {
		t.Fatal("recovery not idempotent", n, err)
	}
}
func TestFailedRecoveryDoesNotPublishPendingRuns(t *testing.T) {
	r := runsreg.New()
	r.Mirror = false
	r.Alive = func(int) bool { return true }
	r.StartTime = func(int) (time.Time, bool) { return time.Time{}, false }
	cause := errors.New("synthetic commit failure")
	n, err := r.Restore(func(f func([]runs.Entry) ([]runs.Entry, error)) error {
		_, err := f([]runs.Entry{{PID: 42, ID: "pending", Tag: "legacy"}})
		if err != nil {
			return err
		}
		return cause
	})
	if n != 0 || !errors.Is(err, cause) {
		t.Fatal(n, err)
	}
	if _, ok := r.Lookup(42); ok {
		t.Fatal("failed transaction published ownership")
	}
}
