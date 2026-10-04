package groups

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/runs"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// The oracle intentionally shares neither Ancestor nor the memoized walker.
// Inputs are one immutable publication round, just like DirectRuns itself.
func referenceDirectOwner(pid int, parents map[int]int, entries map[int]runs.Entry) (state.Run, bool) {
	for steps := 0; pid > 1 && steps < ports.MaxAncestry; steps++ {
		if e, ok := entries[pid]; ok {
			return state.Run{ID: e.ID, Group: e.GroupOf(), Name: e.NameOf(), RootPID: e.PID}, true
		}
		parent, ok := parents[pid]
		if !ok || parent == pid {
			break
		}
		pid = parent
	}
	return state.Run{}, false
}

func TestDirectRunsRandomGraphsMatchIndependentOracle(t *testing.T) {
	for seed := int64(0); seed < 100; seed++ {
		rng := rand.New(rand.NewSource(seed))
		parents := map[int]int{}
		entries := map[int]runs.Entry{}
		for pid := 2; pid < 258; pid++ {
			if rng.Intn(7) != 0 {
				parents[pid] = rng.Intn(260)
			}
			if rng.Intn(19) == 0 {
				entries[pid] = runs.Entry{PID: pid, ID: fmt.Sprint(pid), Group: "project", Name: "service"}
			}
		}
		// Include dense chains beyond the observation budget as well as the
		// arbitrary functional graphs' cycles, missing links and near owners.
		if seed%3 == 0 {
			for pid := 3; pid < 150; pid++ {
				parents[pid] = pid - 1
				delete(entries, pid)
			}
			entries[2] = runs.Entry{PID: 2, ID: "root", Tag: "root"}
		}
		d := newDirectRuns(&runs.Registry{Runs: entries}, parents)
		d.startedAt = nil
		queries := rng.Perm(262)
		for repeat := 0; repeat < 2; repeat++ {
			for _, pid := range queries {
				want, found := referenceDirectOwner(pid, parents, entries)
				got, ok := d.Run(state.Port{PID: pid})
				if ok != found || (found && got != want) {
					t.Fatalf("seed=%d repeat=%d pid=%d: got (%+v,%v), want (%+v,%v)", seed, repeat, pid, got, ok, want, found)
				}
			}
		}
	}
}

var scratchBenchmarkRun state.Run
var scratchBenchmarkFound bool

// Only observation memoization is cleared between cold queries. Registry and
// process-table construction, process I/O and benchmark setup are excluded.
// Report this as a walker microbenchmark, never as end-to-end scan throughput.
func BenchmarkDirectRunsScratch(b *testing.B) {
	for _, depth := range []int{1, 4, 32, 64, 96} {
		for _, owned := range []bool{false, true} {
			name := fmt.Sprintf("depth=%d/owned=%v", depth, owned)
			for _, warm := range []bool{false, true} {
				b.Run(fmt.Sprintf("%s/warm=%v", name, warm), func(b *testing.B) {
					parents := map[int]int{}
					for pid := 1001; pid < 1000+depth; pid++ {
						parents[pid] = pid - 1
					}
					owner := 9999
					if owned {
						owner = 1000
					}
					d := testDirectRuns(parents, runs.Entry{PID: owner, ID: "run", Tag: "project"})
					d.startedAt = nil
					leaf := 999 + depth
					d.runForPID(leaf) // Allocate memo storage outside the timed loop.
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if !warm {
							clear(d.resolved)
						}
						scratchBenchmarkRun, scratchBenchmarkFound = d.runForPID(leaf)
					}
				})
			}
		}
	}
}
