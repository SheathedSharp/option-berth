package groupstart

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
)

// dependencySet is the pair every case here waits for: one ported dependency
// that comes up first and one that comes up second.
func dependencySet() []groups.Service {
	return []groups.Service{
		{Name: "db", Port: 5432},
		{Name: "cache", Port: 6379},
	}
}

func dependencyBook() *addressBook {
	return &addressBook{ports: map[string]int{"db": 5432, "cache": 6379}}
}

// scanCalls is a scanner whose listener table is a function of how many times
// it has been asked. Invalidating after each answer keeps the loop from serving
// a cached snapshot, so each poll sees the next step of the story.
func scanCalls(t *testing.T, answer func(call int) ([]ports.ListeningPort, error)) (*scanner.Loop, func() int) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	var l *scanner.Loop
	l = scanner.New(scanner.Options{ScanContext: func(context.Context, scanner.Include) ([]ports.ListeningPort, error) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		l.Invalidate()
		return answer(call)
	}})
	return l, func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

// TestWaitingOnShrinksAsDependenciesBecomeReady is the point of the progress
// callback: the reader-visible set follows the poll instead of staying at the
// declaration the start began with.
func TestWaitingOnShrinksAsDependenciesBecomeReady(t *testing.T) {
	setDependencyBudget(t, 2*time.Second)
	l, calls := scanCalls(t, func(call int) ([]ports.ListeningPort, error) {
		switch call {
		case 1:
			return nil, nil
		case 2:
			return []ports.ListeningPort{{Port: 5432, PID: 123, Tag: "db", RunGroup: "demo"}}, nil
		default:
			return []ports.ListeningPort{
				{Port: 5432, PID: 123, Tag: "db", RunGroup: "demo"},
				{Port: 6379, PID: 124, Tag: "cache", RunGroup: "demo"},
			}, nil
		}
	})
	rt := &daemon.Runtime{Scanner: l}

	var observed [][]string
	err := waitForProgress(context.Background(), rt, "demo", dependencySet(), dependencyBook(), "",
		func(names []string) {
			observed = append(observed, append([]string(nil), names...))
		})
	if err != nil {
		t.Fatalf("wait = %v, want both dependencies ready", err)
	}
	if calls() < 3 {
		t.Fatalf("scanner calls = %d, want at least three polls", calls())
	}
	want := [][]string{{"db", "cache"}, {"cache"}, nil}
	if len(observed) != len(want) {
		t.Fatalf("waiting_on updates = %v, want %v", observed, want)
	}
	for i := range want {
		if strings.Join(observed[i], ",") != strings.Join(want[i], ",") || (observed[i] == nil) != (want[i] == nil) {
			t.Fatalf("waiting_on update %d = %v, want %v (all: %v)", i, observed[i], want[i], observed)
		}
	}
}

// TestWaitingOnKeepsLastSetWhenObservationIsUnknown: an unreadable snapshot is
// "unknown", so the last known set stays published. An empty set would claim
// nothing is being waited on, which is the opposite of what happened.
func TestWaitingOnKeepsLastSetWhenObservationIsUnknown(t *testing.T) {
	setDependencyBudget(t, 300*time.Millisecond)
	unreadable := errors.New("netstat: permission denied")
	l, _ := scanCalls(t, func(int) ([]ports.ListeningPort, error) {
		return nil, unreadable
	})
	rt := &daemon.Runtime{Scanner: l}
	attempt := beginStartAttempt(rt, "demo", "start-1", groups.Service{Name: "web"},
		dependencyNames(dependencySet()), nil)
	defer attempt.finish()

	err := waitForProgress(context.Background(), rt, "demo", dependencySet(), dependencyBook(), "start-1",
		attempt.setWaitingOn)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("wait = %v, want a dependency timeout", err)
	}
	got := attempt.progressChunk().WaitingOn
	if strings.Join(got, ",") != "db,cache" {
		t.Fatalf("waiting_on = %v, want the declared set kept through an unknown observation", got)
	}
}

// TestReportPendingNeverReportsUnknownAsEmpty pins the rule at the callback
// boundary, independent of the wait loop.
func TestReportPendingNeverReportsUnknownAsEmpty(t *testing.T) {
	var got [][]string
	record := func(names []string) { got = append(got, names) }

	reportPending([]pendingDependency{{Display: "fresh dependency state: boom"}}, record)
	if len(got) != 0 {
		t.Fatalf("an unknown observation reported %v, want no update", got)
	}

	reportPending([]pendingDependency{{Name: "db"}, {Name: "cache"}}, record)
	reportPending([]pendingDependency{{Name: "cache"}}, record)
	reportPending(nil, record)
	want := [][]string{{"db", "cache"}, {"cache"}, nil}
	if len(got) != len(want) {
		t.Fatalf("updates = %v, want %v", got, want)
	}
	for i := range want {
		if strings.Join(got[i], ",") != strings.Join(want[i], ",") || (got[i] == nil) != (want[i] == nil) {
			t.Fatalf("update %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestSetWaitingOnDoesNotTouchPhaseOrOutliveTheAttempt: the dependency poll only
// updates what is being waited on, and a poll landing after the attempt ended
// must not resurrect it.
func TestSetWaitingOnDoesNotTouchPhaseOrOutliveTheAttempt(t *testing.T) {
	rt := &daemon.Runtime{}
	attempt := beginStartAttempt(rt, "demo", "start-1", groups.Service{Name: "web"}, []string{"db", "cache"}, nil)
	defer attempt.finish()

	attempt.setWaitingOn([]string{"cache"})
	if got := attempt.progressChunk(); got.Phase != phaseWaitingDependency || strings.Join(got.WaitingOn, ",") != "cache" {
		t.Fatalf("chunk after setWaitingOn = %+v, want the same phase and only cache", got)
	}

	attempt.setPhase(phasePrepare, nil)
	if got := attempt.progressChunk(); len(got.WaitingOn) != 0 {
		t.Fatalf("prepare kept waiting_on = %v, want it cleared", got.WaitingOn)
	}

	attempt.finish()
	attempt.setWaitingOn([]string{"db"})
	if _, ok := lookupStartAttempt(rt, "demo", "web"); ok {
		t.Fatal("a finished attempt was recreated by setWaitingOn")
	}
}
