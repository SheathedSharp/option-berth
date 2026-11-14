package runsreg

import (
	"testing"
	"time"
)

func TestGroupStopCaptureKeepsSelectedGenerationAndReceipt(t *testing.T) {
	r := New()
	r.Mirror = false
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.Register(Record{ID: "old", PID: 424242, Group: "demo", Name: "worker", StartedAt: at})
	captured := r.CaptureGroupStop("demo", "stopped")
	if len(captured.Runs) != 1 || captured.Runs[0].Owner.ID != "old" || !captured.Runs[0].StartedAt.Equal(at) || captured.OnSignal == nil {
		t.Fatalf("capture = %+v", captured.Runs)
	}
	r.Register(Record{ID: "new", PID: 424242, Group: "demo", Name: "worker", StartedAt: at.Add(time.Minute)})
	captured.OnSignal(424242)
	current, _ := r.Lookup(424242)
	if current.ID != "new" || current.stoppingReason != "" || current.stopping {
		t.Fatalf("old receipt modified replacement: %+v", current)
	}
	if captured.Runs[0].Owner.ID != "old" || !captured.Runs[0].StartedAt.Equal(at) {
		t.Fatal("capture changed after registration")
	}
}

func TestGroupStopCaptureIncludesHiddenRunsWithoutProbing(t *testing.T) {
	r := New()
	r.Mirror = false
	r.Alive = func(int) bool { t.Fatal("capture must not probe processes"); return false }
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.Register(Record{ID: "worker", PID: 424242, Group: "DEMO", Name: "worker", StartedAt: at, stopping: true})
	r.Register(Record{ID: "other", PID: 424243, Group: "other", StartedAt: at})
	captured := r.CaptureGroupStop("demo", "ready_timeout")
	if len(captured.Runs) != 1 || captured.Runs[0].Owner.ID != "worker" {
		t.Fatalf("capture = %+v", captured.Runs)
	}
	captured.OnSignal(424242)
	current, _ := r.Lookup(424242)
	if !current.stopping || current.stoppingReason != "ready_timeout" {
		t.Fatalf("receipt lost compatible state: %+v", current)
	}
	other, _ := r.Lookup(424243)
	if other.stoppingReason != "" {
		t.Fatal("receipt affected another group")
	}
}
