package runsreg

import (
	"reflect"
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

// `restart --run` replays the recorded vector, so the live row has to publish
// it. The record keeps the argv; a row built without it tells the caller the
// run predates exact vectors and cannot be restarted, which is false for every
// run this version started.
func TestRunRowsCarryTheExactArgv(t *testing.T) {
	argv := []string{"npm", "run", "dev", "--", "--port", "3000"}
	rec := Record{
		ID: "run-1", PID: 42, Group: "demo", Name: "web",
		Cmd: "npm run dev -- --port 3000", Argv: argv, StartedAt: time.Now(),
	}
	live := row(rec, state.Snapshot{}, true)
	if !reflect.DeepEqual(live.Argv, argv) {
		t.Fatalf("live argv = %+v, want %+v", live.Argv, argv)
	}

	finished := exitRow(Exit{Record: rec, Code: 0, Reason: ReasonExited, ExitedAt: time.Now()}, nil)
	if !reflect.DeepEqual(finished.Argv, argv) {
		t.Fatalf("exit argv = %+v, want %+v", finished.Argv, argv)
	}
}

// A record from a version that did not capture argv has none. The row must keep
// that absence rather than publish an empty vector, which would read as "this
// run was started with no argument vector".
func TestRunRowsKeepAnUnrecordedArgvAbsent(t *testing.T) {
	rec := Record{ID: "run-1", PID: 42, Group: "demo", Name: "web", Cmd: "npm run dev", StartedAt: time.Now()}
	if live := row(rec, state.Snapshot{}, true); len(live.Argv) != 0 {
		t.Fatalf("live argv = %+v, want none for a run that recorded no vector", live.Argv)
	}
}

// A row's argv is its own copy: the caller of runs.list must not be able to
// rewrite the registry's record by mutating what it received.
func TestRunRowsDoNotShareTheArgvSlice(t *testing.T) {
	argv := []string{"npm", "run", "dev"}
	rec := Record{ID: "run-1", PID: 42, Group: "demo", Name: "web", Argv: argv, StartedAt: time.Now()}
	live := row(rec, state.Snapshot{}, true)
	if len(live.Argv) == 0 {
		t.Fatal("row carried no argv")
	}
	live.Argv[0] = "mutated"
	if rec.Argv[0] != "npm" {
		t.Fatalf("record argv changed through the row: %+v", rec.Argv)
	}
}
