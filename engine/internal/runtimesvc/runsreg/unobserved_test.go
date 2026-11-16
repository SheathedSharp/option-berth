package runsreg

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runs"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

func openExitStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "option-berth.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// A run the runtime service judges dead must leave one terminal fact. Prune is
// the single place the registry drops a run nobody reaped; dropping it without
// recording the ending is what makes status fall back to an older exit under
// the same name.
func TestPruneRecordsAnUnobservedEndingBeforeRemovingTheRun(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	db := openExitStore(t)
	reg := New()
	reg.Alive = func(int) bool { return false }
	reg.StartTime = nil
	reg.SetHistoryStore(db)

	started := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	reg.Register(Record{
		ID: "gone", PID: 424242, Group: "demo", Name: "api",
		Cmd: "npm run dev", Cwd: "/work/demo", StartedAt: started,
		LogPath: "/work/demo/api.log", LogOffset: 12,
	})

	reg.Prune()

	exits := reg.Exits()
	if len(exits) != 1 {
		t.Fatalf("Exits = %d, want one terminal fact for the dead run", len(exits))
	}
	got := exits[0]
	if got.ID != "gone" || got.Reason != ReasonEndedUnobserved || got.Code != UnknownExitCode {
		t.Fatalf("unobserved exit = %+v", got)
	}
	if !got.StartedAt.Equal(started) || got.ExitedAt.IsZero() {
		t.Fatalf("exit times = start %v, observed-end %v", got.StartedAt, got.ExitedAt)
	}
	if _, ok := runs.Snapshot().LookupByPID(424242); ok {
		t.Fatal("the mirror kept a run whose ending was recorded")
	}

	rows, err := db.RunExits(0)
	if err != nil {
		t.Fatalf("RunExits: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "gone" || rows[0].Reason != ReasonEndedUnobserved {
		t.Fatalf("durable exits = %+v", rows)
	}

	reg.Prune()
	if len(reg.Exits()) != 1 {
		t.Fatalf("a second prune duplicated the ending: %+v", reg.Exits())
	}
}

// The same terminal fact must be recorded for a run imported from the mirror
// after a runtime service restart: nothing reaps those processes.
func TestRestoreRecordsAnUnobservedEndingForADeadImportedRun(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	started := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	entry := runs.Entry{
		PID: 424243, ID: "imported-gone", Group: "demo", Name: "api",
		Cmd: "npm run dev", Cwd: "/work/demo",
		StartedAt: started.Format(time.RFC3339Nano),
		LogPath:   "/work/demo/api.log", LogOffset: 7,
	}
	if err := runs.Add(entry); err != nil {
		t.Fatalf("writing the mirror: %v", err)
	}
	db := openExitStore(t)
	reg := New()
	reg.Alive = func(int) bool { return false }
	reg.StartTime = nil
	reg.SetHistoryStore(db)

	moved, err := reg.ImportLegacy()
	if err != nil || moved != 0 {
		t.Fatalf("ImportLegacy = %d/%v, want 0/nil", moved, err)
	}
	exits := reg.Exits()
	if len(exits) != 1 || exits[0].ID != "imported-gone" ||
		exits[0].Reason != ReasonEndedUnobserved || exits[0].Code != UnknownExitCode {
		t.Fatalf("imported ending = %+v", exits)
	}
	if _, ok := runs.Snapshot().LookupByPID(entry.PID); ok {
		t.Fatal("the mirror kept an imported run whose ending was recorded")
	}
	if moved, err := reg.ImportLegacy(); err != nil || moved != 0 {
		t.Fatalf("retry = %d/%v", moved, err)
	}
	if len(reg.Exits()) != 1 {
		t.Fatalf("retry duplicated the ending: %+v", reg.Exits())
	}
}

// If the durable append fails, the mirror entry is the only evidence left. It
// must survive so the next startup can retry recording the ending.
func TestPruneKeepsTheMirrorEntryWhenTheDurableAppendFails(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	db := openExitStore(t)
	_ = db.Close()
	reg := New()
	reg.Alive = func(int) bool { return false }
	reg.StartTime = nil
	reg.SetHistoryStore(db)
	reg.Register(Record{ID: "gone", PID: 424244, Group: "demo", Name: "api",
		StartedAt: time.Date(2026, 11, 16, 7, 0, 0, 0, time.UTC)})

	reg.Prune()

	if _, ok := runs.Snapshot().LookupByPID(424244); !ok {
		t.Fatal("a failed durable append destroyed the only recovery evidence")
	}
	if len(reg.Exits()) != 1 {
		t.Fatalf("the in-memory ending was lost: %+v", reg.Exits())
	}
}
