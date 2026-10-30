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
