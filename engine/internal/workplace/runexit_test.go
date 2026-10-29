package workplace

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/runsreg"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// A run the CLI did not wait for — `oberth start -d` — is closed by the daemon
// when it records how the run ended. Without this the block would say `running`
// for good, which is a record that outlives the truth.

func serveStore(t *testing.T, s *store.Store) {
	t.Helper()
	prev := runtime.Load()
	runtime.Store(&daemon.Runtime{
		Store:  s,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { runtime.Store(prev) })
}

func TestRunExitClosesTheBlockThatRecordedIt(t *testing.T) {
	s := testStore(t)
	serveStore(t, s)
	root := "/home/dev/code/demo"
	wp, err := Create(s, root, "default")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	started := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	if _, err := s.Workplaces().AppendBlock(store.BlockRow{
		ID: "b1", Workplace: wp.ID, Kind: "shell", Source: "engine",
		Input: "npm test", Status: "running", RunID: "run-1", PID: 42, StartedAt: started,
	}); err != nil {
		t.Fatalf("AppendBlock: %v", err)
	}

	daemon.NotifyRunExit(daemon.RunExit{
		RunID: "run-1", PID: 42, Code: 2, Reason: runsreg.ReasonCrashed,
		ExitedAt: started.Add(30 * time.Second),
	})

	blocks, err := s.Workplaces().Blocks(wp.ID, 0)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks", len(blocks))
	}
	got := blocks[0]
	if got.Status != "failed" || got.ExitCode == nil || *got.ExitCode != 2 {
		t.Fatalf("the block was not closed with the run's outcome: %+v", got)
	}
	if got.EndedAt.IsZero() {
		t.Fatal("the closed block has no end time")
	}
	// What the start observation recorded is still there: closing a block adds
	// the outcome, it does not replace the record.
	if got.Input != "npm test" || got.RunID != "run-1" || got.PID != 42 {
		t.Fatalf("closing the block lost the run evidence: %+v", got)
	}
}

func TestRunExitIgnoresWhatItCannotAttribute(t *testing.T) {
	s := testStore(t)
	serveStore(t, s)
	root := "/home/dev/code/demo"
	wp, err := Create(s, root, "default")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A block with no run behind it, and a run with no block: neither is an
	// error, and neither may be closed by the other.
	if _, err := s.Workplaces().AppendBlock(store.BlockRow{
		ID: "reported", Workplace: wp.ID, Kind: "shell", Source: "reported", Status: "running",
	}); err != nil {
		t.Fatalf("AppendBlock: %v", err)
	}
	daemon.NotifyRunExit(daemon.RunExit{RunID: "", PID: 7, Code: 0, Reason: runsreg.ReasonExited})
	daemon.NotifyRunExit(daemon.RunExit{RunID: "run-unknown", PID: 8, Code: 0, Reason: runsreg.ReasonExited})

	blocks, err := s.Workplaces().Blocks(wp.ID, 0)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Status != "running" {
		t.Fatalf("a block with no run was closed: %+v", blocks)
	}
}

func TestRunExitWithNoDaemonIsIgnored(t *testing.T) {
	prev := runtime.Load()
	runtime.Store(nil)
	t.Cleanup(func() { runtime.Store(prev) })
	// The hook runs on the reaper's path, where there is no error to return to:
	// an exit arriving with no store behind it must do nothing at all.
	daemon.NotifyRunExit(daemon.RunExit{RunID: "run-1", Code: 0, Reason: runsreg.ReasonExited})
}
