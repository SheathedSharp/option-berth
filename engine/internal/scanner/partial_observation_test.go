package scanner

import (
	"context"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// A partial table is not a failed scan: the readable rows are published, and
// the snapshot says which part is unknown. An absent port must not look free.
func TestPartialScanPublishesReadableRowsWithVerdict(t *testing.T) {
	partial := &ports.ObservationError{
		Code:    ports.CodeObservationIncomplete,
		Detail:  "netstat: unreadable TCP rows: line 7: invalid listening address",
		Hint:    "run `oberth doctor --only listener_observation`",
		Partial: true,
		Rows: []ports.ListeningPort{
			{Port: 18001, PID: 123, BindAddress: "127.0.0.1"},
		},
	}
	l := New(Options{ScanContext: func(context.Context, Include) ([]ports.ListeningPort, error) {
		return partial.Rows, partial
	}})

	snap, err := l.Rescan(Include{})
	if err != nil {
		t.Fatalf("partial scan returned an error: %v", err)
	}
	if len(snap.Ports) != 1 || snap.Ports[0].Port != 18001 {
		t.Fatalf("ports = %+v, want the readable row", snap.Ports)
	}
	if snap.Observation == nil ||
		snap.Observation.State != state.ObservationPartial ||
		snap.Observation.Code != ports.CodeObservationIncomplete {
		t.Fatalf("observation = %+v", snap.Observation)
	}
	if snap.Observation.Hint == "" || snap.Observation.Detail == "" {
		t.Fatalf("observation lost its evidence: %+v", snap.Observation)
	}
}

// A verdict that resolves with no row moving must still reach the wire. Holding
// the stale "partial" back would publish an unreadable table as a whole one for
// as long as the ports stayed the same.
func TestResolvedObservationRepublishesWithoutPortChanges(t *testing.T) {
	partial := &ports.ObservationError{
		Code:    ports.CodeObservationIncomplete,
		Detail:  "netstat: unreadable TCP rows: line 7: invalid listening address",
		Hint:    "run `oberth doctor --only listener_observation`",
		Partial: true,
		Rows:    []ports.ListeningPort{{Port: 18001, PID: 123, BindAddress: "127.0.0.1"}},
	}
	rows := partial.Rows
	scans := 0
	l := New(Options{ScanContext: func(context.Context, Include) ([]ports.ListeningPort, error) {
		scans++
		if scans == 1 {
			return rows, partial
		}
		return rows, nil
	}})

	if snap, err := l.Rescan(Include{}); err != nil || snap.Observation == nil {
		t.Fatalf("first scan = %+v, %v", snap.Observation, err)
	}
	next, prev, changed, err := l.scanLocked(Include{}, Include{}, false)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v, want the resolved verdict published", changed, err)
	}
	if next.Observation != nil || prev.Observation == nil {
		t.Fatalf("next=%+v prev=%+v, want partial -> whole", next.Observation, prev.Observation)
	}
	if len(next.Ports) != 1 || next.Ports[0].Port != 18001 {
		t.Fatalf("ports = %+v, want the unchanged row", next.Ports)
	}
	if next.Seq <= prev.Seq {
		t.Fatalf("seq did not advance: %d -> %d", prev.Seq, next.Seq)
	}
}

// A healthy process re-checked at a later moment is not a change: CheckedAt is
// when the probe ran, not a fact about the table.
func TestRecheckedHealthyObservationPublishesNothing(t *testing.T) {
	rows := []ports.ListeningPort{{Port: 18001, PID: 123, BindAddress: "127.0.0.1"}}
	l := New(Options{ScanContext: func(context.Context, Include) ([]ports.ListeningPort, error) {
		return rows, nil
	}})
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	l.snap.Observation = &state.Observation{State: state.ObservationPartial, CheckedAt: "2026-01-01T00:00:00Z"}
	l.mu.Unlock()
	next, prev, changed, err := l.scanLocked(Include{}, Include{}, false)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v, want the healthy verdict published", changed, err)
	}
	if next.Observation != nil {
		t.Fatalf("observation = %+v, want none", next.Observation)
	}
	if prev.Observation == nil {
		t.Fatal("the previous partial verdict was not carried into prev")
	}
}
