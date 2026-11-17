package cmd

import (
	"strings"
	"testing"
	"time"

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

// The target a named run becomes pins the recorded generation: the root pid plus
// the birth time the host recorded for it. The birth time is what makes the stop
// safe against a recycled pid, so it has to survive into the target.
func TestRunTargetsPinTheRecordedGeneration(t *testing.T) {
	rec := rpc.RunRecord{
		ID: "run-a", PID: 4242, Group: "shop", Name: "api", Status: "running",
		StartedAt: "2026-11-16T09:00:00Z",
	}
	targets := runTargets([]rpc.RunRecord{rec})
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
