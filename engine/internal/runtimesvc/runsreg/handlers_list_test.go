package runsreg

import (
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// A blind collector makes ports unknown; it must not turn every port-hinted
// run into "starting". The run's identity and log path are still facts.
func TestRowKeepsABlindRunRunning(t *testing.T) {
	rec := Record{
		ID: "run-1", PID: 42, Group: "demo", Name: "api",
		PortHint: 3000, StartedAt: time.Now(),
	}
	blind := row(rec, state.Snapshot{}, false)
	if blind.Status != "running" {
		t.Fatalf("blind status = %q, want running", blind.Status)
	}
	if len(blind.Ports) != 0 {
		t.Fatalf("blind ports = %v, want none", blind.Ports)
	}
	if blind.LogPath != rec.LogPath || blind.ID != rec.ID {
		t.Fatalf("blind row lost run identity: %+v", blind)
	}

	known := row(rec, state.Snapshot{}, true)
	if known.Status != "starting" {
		t.Fatalf("known status = %q, want starting when the hinted port is absent", known.Status)
	}
}

// The run-scoped log read needs the offset to be on the wire; losing it here
// would make `logs --run current` silently read every earlier generation.
func TestRunRowsCarryTheLogOffset(t *testing.T) {
	rec := Record{
		ID: "run-1", PID: 42, Group: "demo", Name: "api",
		LogPath: "/tmp/api.log", LogOffset: 128, StartedAt: time.Now(),
	}
	live := row(rec, state.Snapshot{}, true)
	if live.LogOffset == nil || *live.LogOffset != 128 {
		t.Fatalf("live log_offset = %v, want 128", live.LogOffset)
	}

	finished := exitRow(Exit{Record: rec, Code: 1, Reason: ReasonCrashed, ExitedAt: time.Now()}, nil)
	if finished.LogOffset == nil || *finished.LogOffset != 128 {
		t.Fatalf("exit log_offset = %v, want 128", finished.LogOffset)
	}
}
