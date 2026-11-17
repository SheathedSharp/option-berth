package cmd

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runs"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// A dead run still in the mirror is the newest generation. Its unobserved
// ending must outrank the older crash under the same name, and the older crash
// must not be presented as the current failure.
func TestUnobservedEndingOutranksAnOlderExitUnderTheSameName(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	now := time.Now().UTC()
	if err := runs.Add(runs.Entry{
		PID: 999999999, ID: "new", Group: "demo", Name: "api",
		StartedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("writing the mirror: %v", err)
	}
	olderCode := 1
	older := rpc.RunRecord{
		ID: "old", PID: 111, Group: "demo", Name: "api", Status: "exited",
		Reason: "crashed", ExitCode: &olderCode,
		StartedAt: now.Add(-2 * time.Hour).Format(time.RFC3339),
		ExitedAt:  now.Add(-90 * time.Minute).Format(time.RFC3339),
	}
	doc := statusDocument{
		Worktree: state.Group{Services: []state.Service{{
			Name: "api", LastExit: &state.ServiceExit{
				Code: state.ExitCodeUnknown, Reason: state.ExitEndedUnobserved, RunID: "new",
			},
		}}},
		Exits: mergeExitRecords([]rpc.RunRecord{older}, unobservedEndings("demo")),
	}
	sortStatusExits(doc.Exits)
	if len(doc.Exits) != 2 {
		t.Fatalf("exits = %d, want the durable crash and the derived ending", len(doc.Exits))
	}
	if doc.Exits[0].ID != "new" || doc.Exits[0].Reason != state.ExitEndedUnobserved {
		t.Fatalf("newest exit = %+v, want the unobserved ending", doc.Exits[0])
	}
	if failures := currentFailures(doc); len(failures) != 0 {
		t.Fatalf("failure section = %+v, want the older crash suppressed", failures)
	}
}

func TestMergeExitRecordsLetsTheDurableExitWin(t *testing.T) {
	code := 3
	durable := rpc.RunRecord{ID: "same", Reason: "crashed", ExitCode: &code}
	derived := rpc.RunRecord{ID: "same", Reason: state.ExitEndedUnobserved}
	got := mergeExitRecords([]rpc.RunRecord{durable}, []rpc.RunRecord{derived})
	if len(got) != 1 || got[0].Reason != "crashed" {
		t.Fatalf("merged = %+v, want the durable record only", got)
	}
}

// The service row is built from directRunHistory when no host answers. Its
// reducer keeps the first exit per service, so the derived ending must be
// ordered by the run's start; otherwise the older durable crash stays the
// service's last outcome.
func TestLocalServiceFactsPreferTheUnobservedEnding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	// BERTH_DB overrides BERTH_HOME for the database; without it this test
	// would write into the shared test home and see another test's exits.
	t.Setenv("BERTH_DB", filepath.Join(home, "option-berth.db"))
	base := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	db, err := store.Open(store.Path())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.AppendRunExit(store.RunExitRow{
		ID: "old", PID: 111, Group: "demo", Name: "api",
		Code: 1, Reason: "crashed",
		StartedAt: base.Add(-time.Hour), ExitedAt: base,
	}); err != nil {
		t.Fatalf("AppendRunExit: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runs.Add(runs.Entry{
		PID: 999999999, ID: "new", Group: "demo", Name: "api",
		StartedAt: base.Add(time.Minute).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("writing the mirror: %v", err)
	}

	history, err := directRunHistory(context.Background())
	if err != nil {
		t.Fatalf("directRunHistory: %v", err)
	}
	evidence, ok := history.ServiceFacts()[servicefacts.Key{Group: "demo", Service: "api"}]
	if !ok || evidence.LastExit == nil {
		t.Fatalf("service facts = %+v, want the api exit", evidence)
	}
	if evidence.LastExit.Reason != state.ExitEndedUnobserved || evidence.LastExit.RunID != "new" {
		t.Fatalf("last exit = %+v, want the unobserved ending", evidence.LastExit)
	}
}
