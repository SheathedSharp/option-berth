package groups

import (
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/runs"
	"github.com/sheathedsharp/option-berth/internal/state"
)

func testDirectRuns(parents map[int]int, entries ...runs.Entry) *DirectRuns {
	reg := &runs.Registry{Runs: map[int]runs.Entry{}}
	for _, e := range entries {
		reg.Runs[e.PID] = e
	}
	return newDirectRuns(reg, parents)
}

// TestDirectRunsWalksAncestry: a listener descended from a registered run
// inherits its identity; a run pid resolves directly; an unrelated tree
// resolves to nothing.
func TestDirectRunsWalksAncestry(t *testing.T) {
	parents := map[int]int{100: 1, 200: 100, 300: 200, 400: 300, 500: 1}
	d := testDirectRuns(parents, runs.Entry{PID: 100, Tag: "demo", ID: "chg-1", Cmd: "npm run dev"})

	run, ok := d.Run(state.Port{PID: 400})
	if !ok || run.ID != "chg-1" || run.Name != "demo" || run.RootPID != 100 {
		t.Fatalf("descendant = %+v ok=%v, want demo/chg-1 rooted at 100", run, ok)
	}
	if run, ok := d.Run(state.Port{PID: 100}); !ok || run.Name != "demo" {
		t.Errorf("direct pid = %+v ok=%v, want the run itself", run, ok)
	}
	if run, ok := d.Run(state.Port{PID: 500}); ok {
		t.Errorf("unrelated pid = %+v, want no run", run)
	}
}

// TestDirectRunsIgnoresAStaleStamp: the row's own fields are not a second
// source. A port carrying an old stamp stays unattributed when the registry
// no longer knows any owning pid.
func TestDirectRunsIgnoresAStaleStamp(t *testing.T) {
	stale := state.Run{ID: "old", Name: "old"}
	d := testDirectRuns(map[int]int{7: 1})
	if run, ok := d.Run(state.Port{PID: 7, Run: &stale}); ok {
		t.Fatalf("stale stamp answered as ownership: %+v", run)
	}
}

func TestDirectRunsEmptyRegistryShortCircuits(t *testing.T) {
	d := testDirectRuns(map[int]int{42: 1})
	if run, ok := d.Run(state.Port{PID: 42}); ok {
		t.Fatalf("empty registry resolved %+v", run)
	}
}

// TestDirectRunsCachesWalkResults: every pid visited on a walk shares its
// endpoint, so a sibling listener is answered from the first walk.
func TestDirectRunsCachesWalkResults(t *testing.T) {
	parents := map[int]int{100: 1, 200: 100, 300: 200, 400: 300, 600: 1}
	d := testDirectRuns(parents, runs.Entry{PID: 100, Tag: "demo"})

	d.Run(state.Port{PID: 400})
	for _, pid := range []int{400, 300, 200, 100} {
		if hit, ok := d.resolved[pid]; !ok || !hit.ok {
			t.Errorf("resolved[%d] = %+v, want a positive hit", pid, hit)
		}
	}
	d.Run(state.Port{PID: 600})
	if hit, ok := d.resolved[600]; !ok || hit.ok {
		t.Errorf("resolved[600] = %+v, want a cached miss", hit)
	}
	if _, ok := d.resolved[500]; ok {
		t.Error("a pid outside every walk was cached")
	}
}

func TestDirectRunsCycleGuard(t *testing.T) {
	// Pathological cycle: 10 <-> 11. Must terminate with no owner.
	d := testDirectRuns(map[int]int{10: 11, 11: 10}, runs.Entry{PID: 100, Tag: "t"})
	if run, ok := d.Run(state.Port{PID: 10}); ok {
		t.Fatalf("cycle resolved %+v", run)
	}
}

// TestDirectRunsIgnoresARecycledPID: the direct path applies the same identity
// rule as the daemon registry — an entry whose recorded process is gone and
// whose pid now names a newer process does not own the listener. Unknown (no
// live answer) keeps the entry.
func TestDirectRunsIgnoresARecycledPID(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	entry := runs.Entry{PID: 100, Tag: "demo", ID: "run-1", StartedAt: base.Format(time.RFC3339)}
	parents := map[int]int{100: 1}

	d := testDirectRuns(parents, entry)
	d.startedAt = func(int) (time.Time, bool) { return base.Add(time.Hour), true }
	if run, ok := d.Run(state.Port{PID: 100}); ok {
		t.Fatalf("recycled pid attributed to %+v", run)
	}

	d = testDirectRuns(parents, entry)
	d.startedAt = func(int) (time.Time, bool) { return base.Add(time.Second), true }
	if _, ok := d.Run(state.Port{PID: 100}); !ok {
		t.Fatal("a start within tolerance lost its run")
	}

	d = testDirectRuns(parents, entry)
	d.startedAt = func(int) (time.Time, bool) { return time.Time{}, false }
	if _, ok := d.Run(state.Port{PID: 100}); !ok {
		t.Fatal("an unknown live start time dropped the run")
	}
}
