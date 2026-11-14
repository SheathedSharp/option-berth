package runsreg

import (
	"os"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/codeid"
)

func saveTestSnapshot(t *testing.T, runID string) {
	t.Helper()
	err := codeid.Save(runID, codeid.Snapshot{
		Version: codeid.SnapshotVersion,
		Root:    t.TempDir(),
		Sources: []string{"."},
		Digest:  "digest-" + runID,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The snapshot belongs to the run, not to the BERTH_HOME directory forever: a
// run that leaves the registry must take its snapshot with it.
func TestExitRemovesTheRunsCodeSnapshot(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	saveTestSnapshot(t, "run-exit")
	r := New()
	r.Mirror = false
	rec := Record{ID: "run-exit", PID: 424242, Group: "demo", Name: "api", StartedAt: time.Now()}
	r.Register(rec)
	if _, ok := r.ExitedRun(rec, 0, true); !ok {
		t.Fatal("the registered run did not exit")
	}
	if _, err := codeid.Load("run-exit"); !os.IsNotExist(err) {
		t.Fatalf("snapshot survived the run: %v", err)
	}
}

// A daemon crash can leave snapshots for runs that are gone. Startup pruning
// keeps exactly the live registry's ids.
func TestPruneCodeSnapshotsKeepsOnlyLiveRuns(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	saveTestSnapshot(t, "run-live")
	saveTestSnapshot(t, "run-gone")
	r := New()
	r.Mirror = false
	r.Register(Record{ID: "run-live", PID: 424243, Group: "demo", Name: "api", StartedAt: time.Now()})
	r.PruneCodeSnapshots()
	if _, err := codeid.Load("run-live"); err != nil {
		t.Fatalf("live run's snapshot was pruned: %v", err)
	}
	if _, err := codeid.Load("run-gone"); !os.IsNotExist(err) {
		t.Fatalf("orphan snapshot survived pruning: %v", err)
	}
}
