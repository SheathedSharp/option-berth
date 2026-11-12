package runsreg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// TestExitRowDistinguishesMissingAndMinimumPortHint pins both sides of the
// port-hint boundary: a run that named no port must not be given a URL or a
// holder of its own, and the smallest port a caller can name is still carried
// through. A boundary that only held on one side would either invent a listener
// for every run or drop the one port hint a real run has.
func TestExitRowDistinguishesMissingAndMinimumPortHint(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("fixture", 8*60*60))
	for _, tc := range []struct {
		name string
		hint int
	}{
		{"missing", 0},
		{"minimum", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := exitRow(Exit{
				Record:   Record{ID: "port-boundary", Group: "demo", Name: "api", PortHint: tc.hint},
				Code:     7,
				Reason:   ReasonCrashed,
				ExitedAt: at,
			}, nil)
			// The exit's own facts are the same whatever it declared: how it
			// ended, when, and that it no longer holds a port.
			if got.Status != "exited" || got.ExitCode == nil || *got.ExitCode != 7 ||
				got.Reason != ReasonCrashed || got.ExitedAt != at.UTC().Format(time.RFC3339) {
				t.Fatalf("exit facts = %+v", got)
			}
			if len(got.Ports) != 0 || got.PortHolder != nil {
				t.Fatalf("exit invented a live listener: %+v", got)
			}
			if tc.hint == 0 {
				if got.PortHint != nil || got.URL != "" {
					t.Fatalf("missing hint became a listener URL: %+v", got)
				}
				return
			}
			if got.PortHint == nil || *got.PortHint != 1 || got.URL != "http://localhost:1" {
				t.Fatalf("minimum port hint was lost: %+v", got)
			}
		})
	}
}

// TestExitedRecordsHowARunEnded: code 0 exited, anything else crashed, and a
// run option-berth was stopping is stopped whatever its code.
func TestExitedRecordsHowARunEnded(t *testing.T) {
	reg := testRegistry(11, 12, 13)
	registerAll(reg,
		Record{ID: "api-run", PID: 11, Group: "shop", Name: "api"},
		Record{ID: "web-run", PID: 12, Group: "shop", Name: "web"},
		Record{ID: "job-run", PID: 13, Group: "shop", Name: "job"},
	)
	reg.Stopping([]int{12})

	for _, tc := range []struct {
		pid    int
		code   int
		reason string
	}{
		{11, 1, ReasonCrashed},
		{12, 143, ReasonStopped},
		{13, 0, ReasonExited},
	} {
		e, ok := reg.Exited(tc.pid, tc.code, false)
		if !ok {
			t.Errorf("Exited(%d, %d) recorded nothing", tc.pid, tc.code)
			continue
		}
		if e.Reason != tc.reason || e.Code != tc.code {
			t.Errorf("Exited(%d, %d) = %+v, want reason %s and the code it exited with",
				tc.pid, tc.code, e, tc.reason)
		}
	}
	if live := reg.List(); len(live) != 0 {
		t.Errorf("List = %v, want the exited runs gone from the live ones", runIDs(live))
	}
	exits := reg.Exits()
	if len(exits) != 3 || exits[0].ID != "job-run" {
		t.Fatalf("Exits = %+v, want three, newest first", exits)
	}
	if _, ok := reg.Exited(11, 0, false); ok {
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
	reg := testRegistry(11)
	reg.Register(Record{ID: "api-run", PID: 11, Group: "shop", Name: "api"})
	if _, ok := reg.Exited(11, 143, false); !ok {
		t.Fatal("Exited reported nothing")
	}
	if reason := reg.Exits()[0].Reason; reason != ReasonCrashed {
		t.Fatalf("reason = %s, want it to look like a crash first", reason)
	}

	reg.Stopping([]int{11})
	if reason := reg.Exits()[0].Reason; reason != ReasonStopped {
		t.Errorf("reason = %s after the kill was recorded, want %s", reason, ReasonStopped)
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
	reg := testRegistry(11, 12)
	reg.Register(Record{ID: "api-run", PID: 11, Group: "shop", Name: "api"})
	reg.Exited(11, 1, false)

	e, ok := reg.LastExit("shop", "api")
	if !ok {
		t.Fatal("the exited service reported no last exit")
	}
	if e.Code != 1 || e.Reason != ReasonCrashed || e.RunID != "api-run" || e.At == "" {
		t.Fatalf("LastExit = %+v, want the crashed run and the time it ended", e)
	}
	if e, ok := reg.LastExit("shop", "web"); ok {
		t.Errorf("a service that never ran reported %+v", e)
	}

	reg.Register(Record{ID: "api-again", PID: 12, Group: "shop", Name: "api"})
	if e, ok := reg.LastExit("shop", "api"); ok {
		t.Errorf("a running service reported its last exit: %+v", e)
	}
}

// The exit history is bounded: once more runs than the cap have exited, the
// oldest records make way for the newest.
func TestExitHistoryIsBounded(t *testing.T) {
	reg := testRegistry()
	for pid := 1; pid <= maxExits+5; pid++ {
		reg.Register(Record{ID: itoa(pid), PID: pid, Group: "shop", Name: "job"})
		reg.Exited(pid, 0, false)
	}

	kept := reg.Exits()
	if len(kept) != maxExits {
		t.Fatalf("Exits = %d, want it capped at %d", len(kept), maxExits)
	}
	if kept[0].PID != maxExits+5 {
		t.Errorf("newest exit = %d, want the last one recorded", kept[0].PID)
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

// A rename carries the group's exits with it, so the row for the service keeps
// saying why it stopped.
func TestRenameGroupsMovesTheExitsToo(t *testing.T) {
	reg := testRegistry(11)
	reg.Register(Record{ID: "api-run", PID: 11, Group: "old", Name: "api"})
	reg.Exited(11, 1, false)

	reg.RenameGroups(map[string]string{"old": "new"})
	if _, ok := reg.LastExit("new", "api"); !ok {
		t.Errorf("the exit kept the old group: %+v", reg.Exits())
	}
}

// TestTailLinesReadsOnlyThisRun: the log file is appended to across runs, so
// the lines kept with an exit start where the run did.
func TestTailLinesReadsOnlyThisRun(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "api.log")
	earlier := "an earlier run\n"
	if err := os.WriteFile(logPath, []byte(earlier+"first\nsecond\n"), 0o600); err != nil {
		t.Fatalf("writing the run log: %v", err)
	}
	if got := tailLines(logPath, int64(len(earlier)), 20); strings.Join(got, "|") != "first|second" {
		t.Errorf("tailLines = %v, want this run's two lines", got)
	}
	// An offset past the end means the file was rotated: read the new one.
	if got := tailLines(logPath, 1<<20, 20); len(got) != 3 {
		t.Errorf("tailLines after a rotation = %v, want the whole file", got)
	}

	var log strings.Builder
	for i := range 50 {
		fmt.Fprintf(&log, "line %d\n", i)
	}
	if err := os.WriteFile(logPath, []byte(log.String()), 0o600); err != nil {
		t.Fatalf("rewriting the run log: %v", err)
	}
	tail := tailLines(logPath, 0, 20)
	if len(tail) != 20 || tail[19] != "line 49" {
		t.Errorf("tailLines = %d lines ending %q, want the last 20", len(tail), tail[len(tail)-1])
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
