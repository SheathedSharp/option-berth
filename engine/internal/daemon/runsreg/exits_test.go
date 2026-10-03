package runsreg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/store"
)

// TestExitedRecordsHowARunEnded: code 0 exited, anything else crashed, and a
// run option-berth was stopping is stopped whatever its code.
func TestExitedRecordsHowARunEnded(t *testing.T) {
	r := testRegistry(1, 2, 3)
	for _, rec := range []Record{
		{ID: "a", PID: 1, Group: "g", Name: "api"},
		{ID: "b", PID: 2, Group: "g", Name: "web"},
		{ID: "c", PID: 3, Group: "g", Name: "job"},
	} {
		r.Register(rec)
	}
	r.Stopping([]int{2})

	for _, tt := range []struct {
		pid, code int
		want      string
	}{
		{1, 1, ReasonCrashed},
		{2, 143, ReasonStopped},
		{3, 0, ReasonExited},
	} {
		e, ok := r.Exited(tt.pid, tt.code, false)
		if !ok || e.Reason != tt.want || e.Code != tt.code {
			t.Errorf("Exited(%d, %d) = %+v, %v; want reason %s", tt.pid, tt.code, e, ok, tt.want)
		}
	}
	if got := r.List(); len(got) != 0 {
		t.Errorf("List = %+v, want the exited runs gone from the live ones", got)
	}
	exits := r.Exits()
	if len(exits) != 3 || exits[0].ID != "c" {
		t.Fatalf("Exits = %+v, want three, newest first", exits)
	}
	if _, ok := r.Exited(1, 0, false); ok {
		t.Error("the same run was recorded as exited twice")
	}
}

func TestExitedClassifiesACompetingPortFromTheRunLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.log")
	if err := os.WriteFile(path, []byte("clash: could not bind: address already in use\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := testRegistry(1)
	r.Register(Record{ID: "a", PID: 1, Group: "g", Name: "api", LogPath: path})
	e, ok := r.Exited(1, 1, false)
	if !ok || e.Reason != ReasonPortOccupied {
		t.Fatalf("Exited = %+v, %v; want %q", e, ok, ReasonPortOccupied)
	}
}

func TestStartFailedRecordsServiceEvidence(t *testing.T) {
	r := testRegistry()
	at := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return at }
	e := r.StartFailed(Record{
		Group: "shop", Name: "api", Cmd: "missing-server", Cwd: "/home/me/code/shop",
	}, "missing-server is not on PATH")
	if e.ID == "" || e.PID != 0 || e.Code != 1 || e.Reason != ReasonStartFailed {
		t.Fatalf("start failure = %+v, want a synthetic failed attempt", e)
	}
	if len(e.LastLines) != 1 || e.LastLines[0] != "missing-server is not on PATH" {
		t.Fatalf("start failure lines = %q", e.LastLines)
	}
	last, ok := r.LastExit("shop", "api")
	if !ok || last.Code != 1 || last.Reason != ReasonStartFailed || last.RunID != e.ID {
		t.Fatalf("LastExit = %+v, %v", last, ok)
	}
}

func TestStartFailedWithReasonPreservesDependencyOutcome(t *testing.T) {
	r := testRegistry()
	e := r.StartFailedWithReason(Record{Group: "shop", Name: "api"}, ReasonDependencyTimeout, "timed out waiting for db")
	if e.Reason != ReasonDependencyTimeout {
		t.Fatalf("start failure = %+v, want dependency timeout", e)
	}
	last, ok := r.LastExit("shop", "api")
	if !ok || last.Reason != ReasonDependencyTimeout {
		t.Fatalf("LastExit = %+v, %v, want dependency timeout", last, ok)
	}
}

// TestStoppingCorrectsARunThatAlreadyExited: the kill can win the race against
// being marked, so the record is corrected afterwards.
func TestStoppingCorrectsARunThatAlreadyExited(t *testing.T) {
	r := testRegistry(1)
	r.Register(Record{ID: "a", PID: 1, Group: "g", Name: "api"})
	if _, ok := r.Exited(1, 143, false); !ok {
		t.Fatal("Exited reported nothing")
	}
	if got := r.Exits()[0].Reason; got != ReasonCrashed {
		t.Fatalf("reason = %s, want it to look like a crash first", got)
	}
	r.Stopping([]int{1})
	if got := r.Exits()[0].Reason; got != ReasonStopped {
		t.Errorf("reason = %s, want %s", got, ReasonStopped)
	}
}

func TestStoppingWithReasonPersistsReadinessTimeout(t *testing.T) {
	r := testRegistry(1)
	r.Register(Record{ID: "a", PID: 1, Group: "g", Name: "api"})
	r.StoppingWithReason([]int{1}, "ready_timeout")
	e, ok := r.Exited(1, 143, false)
	if !ok || e.Reason != "ready_timeout" {
		t.Fatalf("Exited = %+v, %v; want ready_timeout", e, ok)
	}
}

// TestLastExitOnlyWhileTheServiceIsDown: a service that has been started again
// reports nothing, so a group row never says "crashed" about a running one.
func TestLastExitOnlyWhileTheServiceIsDown(t *testing.T) {
	r := testRegistry(1, 2)
	r.Register(Record{ID: "a", PID: 1, Group: "g", Name: "api"})
	r.Exited(1, 1, false)

	e, ok := r.LastExit("g", "api")
	if !ok || e.Code != 1 || e.Reason != ReasonCrashed || e.RunID != "a" || e.At == "" {
		t.Fatalf("LastExit = %+v, %v", e, ok)
	}
	if _, ok := r.LastExit("g", "web"); ok {
		t.Error("a service that never ran reported an exit")
	}

	r.Register(Record{ID: "b", PID: 2, Group: "g", Name: "api"})
	if _, ok := r.LastExit("g", "api"); ok {
		t.Error("a running service reported its last exit")
	}
}

func TestExitHistoryIsBounded(t *testing.T) {
	r := testRegistry()
	for i := 1; i <= maxExits+5; i++ {
		r.Register(Record{ID: itoa(i), PID: i, Group: "g", Name: "job"})
		r.Exited(i, 0, false)
	}
	exits := r.Exits()
	if len(exits) != maxExits {
		t.Fatalf("Exits = %d, want it capped at %d", len(exits), maxExits)
	}
	if exits[0].PID != maxExits+5 {
		t.Errorf("newest exit = %d, want the last one recorded", exits[0].PID)
	}
}

func TestExitHistorySurvivesRegistryReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "option-berth.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	first := testRegistry(1)
	first.SetHistoryStore(db)
	started := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	first.now = func() time.Time { return started.Add(time.Minute) }
	first.Register(Record{
		ID: "run-1", PID: 1, Group: "shop", Name: "api",
		Cmd: "python3 api.py", Cwd: "/home/me/code/shop", StartedAt: started,
		LogPath: filepath.Join(t.TempDir(), "api.log"),
	})
	if _, ok := first.Exited(1, 7, false); !ok {
		t.Fatal("Exited did not record the run")
	}

	second := testRegistry()
	if err := second.LoadExits(db); err != nil {
		t.Fatalf("LoadExits: %v", err)
	}
	exits := second.Exits()
	if len(exits) != 1 {
		t.Fatalf("reloaded exits = %d, want 1", len(exits))
	}
	if exits[0].ID != "run-1" || exits[0].Code != 7 || exits[0].Reason != ReasonCrashed {
		t.Fatalf("reloaded exit = %+v", exits[0])
	}
	last, ok := second.LastExit("shop", "api")
	if !ok || last.Code != 7 || last.Reason != ReasonCrashed {
		t.Fatalf("LastExit after reload = %+v, %v", last, ok)
	}
}

func TestRenameGroupsMovesTheExitsToo(t *testing.T) {
	r := testRegistry(1)
	r.Register(Record{ID: "a", PID: 1, Group: "old", Name: "api"})
	r.Exited(1, 1, false)
	r.RenameGroups(map[string]string{"old": "new"})
	if _, ok := r.LastExit("new", "api"); !ok {
		t.Errorf("the exit kept the old group: %+v", r.Exits())
	}
}

// TestTailLinesReadsOnlyThisRun: the log file is appended to across runs, so
// the lines kept with an exit start where the run did.
func TestTailLinesReadsOnlyThisRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.log")
	if err := os.WriteFile(path, []byte("an earlier run\nfirst\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := tailLines(path, int64(len("an earlier run\n")), 20); strings.Join(got, "|") != "first|second" {
		t.Errorf("tailLines = %v, want this run's two lines", got)
	}
	// An offset past the end means the file was rotated: read the new one.
	if got := tailLines(path, 1<<20, 20); len(got) != 3 {
		t.Errorf("tailLines after a rotation = %v, want the whole file", got)
	}

	var b strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got := tailLines(path, 0, 20)
	if len(got) != 20 || got[19] != "line 49" {
		t.Errorf("tailLines = %d lines ending %q, want the last 20", len(got), got[len(got)-1])
	}
}

// TestExitForPIDAttributesExitsToTheirRunNotTheirService: with two runs of one
// service overlapping, the newest exit for the service name can belong to the
// other run. The startup window must ask by pid so a live spawn is never
// reported as failed with a sibling's exit.
func TestExitForPIDAttributesExitsToTheirRunNotTheirService(t *testing.T) {
	r := testRegistry(1, 2)
	r.Register(Record{ID: "old", PID: 1, Group: "g", Name: "server"})
	r.Register(Record{ID: "new", PID: 2, Group: "g", Name: "server"})

	if _, ok := r.Exited(1, 1, false); !ok {
		t.Fatal("recording the old run's exit failed")
	}
	if _, ok := r.ExitForPID(1); !ok {
		t.Fatal("ExitForPID did not find the recorded exit")
	}
	if _, ok := r.ExitForPID(2); ok {
		t.Fatal("ExitForPID attributed the old run's exit to the new pid")
	}

	// The service-level view still reports the newest exit, which is the
	// different, older answer — exactly why the startup window must not use it.
	exit, ok := r.LatestExit("g", "server")
	if !ok || exit.PID != 1 {
		t.Fatalf("LatestExit = %+v/%v, want the recorded run", exit, ok)
	}
}
