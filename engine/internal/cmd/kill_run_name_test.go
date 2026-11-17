package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/killer"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// A run is a subject like a port or a pid, so the positional vocabulary has to
// tell the two apart without guessing: a number is a port or pid, anything else
// is a run name or id.
func TestKillSubjectSplitsNumbersFromRunNames(t *testing.T) {
	for _, tc := range []struct {
		name           string
		args           []string
		numbers, names []string
	}{
		{"ports only", []string{"3000", "5432"}, []string{"3000", "5432"}, nil},
		{"names only", []string{"api", "worker"}, nil, []string{"api", "worker"}},
		{"mixed", []string{"3000", "api"}, []string{"3000"}, []string{"api"}},
		{"a run id is a name", []string{"run9"}, nil, []string{"run9"}},
		{"nothing", nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			numbers, names := splitKillSubjects(tc.args)
			if strings.Join(numbers, ",") != strings.Join(tc.numbers, ",") ||
				strings.Join(names, ",") != strings.Join(tc.names, ",") {
				t.Fatalf("numbers=%v names=%v, want %v/%v", numbers, names, tc.numbers, tc.names)
			}
		})
	}
}

// A name resolves to the run it spells, by the same rule `restart --run` uses,
// so the two commands cannot disagree about which run a name means.
func TestNamedRunsResolveByNameAndID(t *testing.T) {
	records := []rpc.RunRecord{
		{ID: "run-a", PID: 1, Group: "shop", Name: "api", Status: "running"},
		{ID: "run-b", PID: 2, Group: "shop", Name: "worker", Status: "running"},
		{ID: "run-c", PID: 3, Group: "elsewhere", Name: "api", Status: "running"},
	}
	got, err := namedRuns(records, "shop", []string{"api", "run-b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "run-a" || got[1].ID != "run-b" {
		t.Fatalf("resolved = %+v, want run-a and run-b", got)
	}
}

// The name is scoped to the worktree the command runs in: a run of the same
// name in another checkout is that checkout's fact, not this one's.
func TestNamedRunsStayInsideTheWorktree(t *testing.T) {
	records := []rpc.RunRecord{{ID: "run-c", PID: 3, Group: "elsewhere", Name: "api", Status: "running"}}
	_, err := namedRuns(records, "shop", []string{"api"})
	code, msg, _ := describe(err)
	if code != "not_found" || !strings.Contains(msg, "api") {
		t.Fatalf("code = %q msg = %q, want the unknown run", code, msg)
	}
}

// A name nobody answers to is not_found, with the deterministic next step a
// refused port or pid gets: look at what is actually running.
func TestNamedRunsReportAnUnknownName(t *testing.T) {
	_, err := namedRuns(nil, "shop", []string{"ghost"})
	code, msg, hint := describe(err)
	if code != "not_found" || !strings.Contains(msg, "ghost") {
		t.Fatalf("code = %q msg = %q, want the unknown run", code, msg)
	}
	if !strings.Contains(hint, "oberth status") {
		t.Fatalf("hint = %q, want the way to list runs", hint)
	}
}

// Two live runs of one name cannot both be the subject: stopping the wrong
// generation is worse than asking again.
func TestNamedRunsRefuseAnAmbiguousName(t *testing.T) {
	records := []rpc.RunRecord{
		{ID: "run-a", PID: 1, Group: "shop", Name: "api", Status: "running"},
		{ID: "run-b", PID: 2, Group: "shop", Name: "api", Status: "running"},
	}
	_, err := namedRuns(records, "shop", []string{"api"})
	code, _, hint := describe(err)
	if code != "ambiguous" || !strings.Contains(hint, "run-a") || !strings.Contains(hint, "run-b") {
		t.Fatalf("code = %q hint = %q, want both run ids offered", code, hint)
	}
}

// A host that owns the run ledger stops a name by the run's identity, not by a
// pid: the generation is captured there, from the record that names it. The
// resolution therefore carries the runs themselves, and the wire selector — which
// has no room for a birth time — is never the shape they travel in.
func TestNamedRunResolutionCarriesTheRunsNotAPid(t *testing.T) {
	records := []rpc.RunRecord{
		{ID: "run-a", PID: 4242, Group: "shop", Name: "api", Status: "running",
			StartedAt: "2026-11-16T09:00:00Z"},
	}
	resolved, err := namedRunResolution(records, "shop", []string{"api"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.targets) != 0 {
		t.Fatalf("targets = %+v, want none: a host stops the run by identity", resolved.targets)
	}
	if len(resolved.runs) != 1 || resolved.runs[0].ID != "run-a" {
		t.Fatalf("runs = %+v, want the run the name spells", resolved.runs)
	}
	if resolved.group != "shop" {
		t.Fatalf("group = %q, want the worktree's group", resolved.group)
	}
	if !resolved.tree {
		t.Fatal("a run is a tree; the resolution has to say so")
	}
}

// The stop a named run becomes once a host answers names the group and the run
// ids and nothing else. A field that selects by service, by expected run or for
// release would widen one run's stop into the group's.
func TestRunStopParamsCannotWidenIntoAGroupStop(t *testing.T) {
	params := runStopParams("shop", []string{"run-a", "run-b"}, killer.Options{Force: true, Grace: 3 * time.Second}, false)
	if params.Name != "shop" {
		t.Errorf("name = %q, want the worktree's group", params.Name)
	}
	if strings.Join(params.Runs, ",") != "run-a,run-b" {
		t.Errorf("runs = %v, want exactly the named runs", params.Runs)
	}
	if len(params.Only) != 0 || len(params.ExpectRuns) != 0 || params.Release {
		t.Errorf("params = %+v, want nothing that widens the stop", params)
	}
	if !params.Force || params.GraceMs != 3000 {
		t.Errorf("params = %+v, want the flags carried", params)
	}
}

// A run that already ended is not a stop subject: the end state the caller asked
// for holds, so it is dropped rather than sent to a host that would call it a
// failure.
func TestLiveRunRecordsDropEndedGenerations(t *testing.T) {
	live := liveRunRecords([]rpc.RunRecord{
		{ID: "run-a", Status: "running"},
		{ID: "run-old", Status: "exited"},
	})
	if len(live) != 1 || live[0].ID != "run-a" {
		t.Fatalf("live = %+v, want only the running generation", live)
	}
}

// Every named generation having ended means there is nothing to stop — not a
// failure and not a stop of some other process that now holds the pid.
func TestNamedRunResolutionReportsNothingToStopWhenEveryRunEnded(t *testing.T) {
	resolved, err := namedRunResolution([]rpc.RunRecord{
		{ID: "run-old", PID: 4242, Group: "shop", Name: "api", Status: "exited",
			StartedAt: "2026-11-16T09:00:00Z"},
	}, "shop", []string{"api"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.runs) != 0 || len(resolved.targets) != 0 {
		t.Fatalf("resolution = %+v, want an empty stop", resolved)
	}
}

// A run is stopped whole, so the flag that would ask for a half stop is a flag
// combination the subject cannot carry. Refusing says so; ignoring it would send
// SIGKILL after a stop the caller asked to keep polite.
func TestNamedRunResolutionRefusesNoEscalate(t *testing.T) {
	_, err := namedRunResolution(nil, "shop", []string{"api"}, true)
	code, msg, _ := describe(err)
	if code != "invalid_params" || !strings.Contains(msg, "--no-escalate") {
		t.Fatalf("code = %q msg = %q, want the refused flag named", code, msg)
	}
}

// The target a named run becomes pins the recorded generation: the root pid plus
// the birth time the host recorded for it. The birth time is what makes the stop
// safe against a recycled pid, so it has to survive into the target.
func TestRunTargetsPinTheRecordedGeneration(t *testing.T) {
	rec := rpc.RunRecord{
		ID: "run-a", PID: 4242, Group: "shop", Name: "api", Status: "running",
		StartedAt: "2026-11-16T09:00:00Z",
	}
	targets, err := runTargets([]rpc.RunRecord{rec})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %+v", targets)
	}
	got := targets[0]
	if got.PID != 4242 || got.Name != "api" {
		t.Fatalf("target = %+v, want the run's root pid and name", got)
	}
	if got.StartedAt.IsZero() || got.StartedAt.Format(time.RFC3339) != "2026-11-16T09:00:00Z" {
		t.Fatalf("started_at = %v, want the recorded birth time", got.StartedAt)
	}
}

// A run whose recorded start is missing cannot be pinned to its generation, so
// it is refused rather than signalled blind — a pid alone may belong to a
// different process by now.
func TestRunTargetsRefuseAnUnpinnedGeneration(t *testing.T) {
	_, err := runTargets([]rpc.RunRecord{{ID: "run-a", PID: 4242, Name: "api", Status: "running"}})
	code, msg, _ := describe(err)
	if code != "not_found" || !strings.Contains(msg, "run-a") {
		t.Fatalf("code = %q msg = %q, want the unpinnable run", code, msg)
	}
}
