package groups

import (
	"math/rand"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/runs"
	"github.com/sheathedsharp/option-berth/internal/state"
)

func deepDirectRuns() *DirectRuns {
	parents := map[int]int{}
	for pid := 3; pid < ports.MaxAncestry+20; pid++ {
		parents[pid] = pid - 1
	}
	return testDirectRuns(parents, runs.Entry{PID: 2, ID: "root", Tag: "project"})
}

func TestDirectRunsDepthMissDoesNotPoisonAncestors(t *testing.T) {
	d := deepDirectRuns()
	if _, ok := d.Run(state.Port{PID: ports.MaxAncestry + 5}); ok {
		t.Fatal("fixture should exceed the walk budget")
	}
	if run, ok := d.Run(state.Port{PID: 10}); !ok || run.RootPID != 2 {
		t.Fatalf("an earlier truncated walk poisoned a shorter walk: %+v, %v", run, ok)
	}
}

func TestDirectRunsCachedHitDoesNotBypassDepthBound(t *testing.T) {
	d := deepDirectRuns()
	if _, ok := d.Run(state.Port{PID: 10}); !ok {
		t.Fatal("fixture should resolve a short walk")
	}
	if run, ok := d.Run(state.Port{PID: ports.MaxAncestry + 5}); ok {
		t.Fatalf("cached ancestor bypassed the depth budget: %+v", run)
	}
}

func TestDirectRunsMatchesUncachedWalkInAnyOrder(t *testing.T) {
	for seed := int64(0); seed < 20; seed++ {
		d := deepDirectRuns()
		d.parents[1000], d.parents[1001] = 1001, 1000
		d.parents[1002] = 1000
		if seed%2 != 0 {
			d.reg.Runs[30] = runs.Entry{PID: 30, ID: "near", Tag: "near"}
		}
		queries := rand.New(rand.NewSource(seed)).Perm(ports.MaxAncestry + 1020)
		for _, pid := range queries {
			want, found := ports.Ancestor(pid, d.parents, func(id int) (state.Run, bool) {
				e, ok := d.reg.LookupByPID(id)
				return state.Run{ID: e.ID, Group: e.GroupOf(), Name: e.NameOf(), RootPID: e.PID}, ok
			})
			got, ok := d.Run(state.Port{PID: pid})
			if ok != found || (found && got != want) {
				t.Fatalf("seed=%d pid=%d: cached=(%+v,%v), uncached=(%+v,%v)", seed, pid, got, ok, want, found)
			}
		}
	}
}
