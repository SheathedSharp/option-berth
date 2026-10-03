package scanner

import (
	"reflect"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestStatsRefreshAllocatesOnlyOnChange(t *testing.T) {
	rows := []state.Port{{PID: 42, Stats: &state.Stats{MemoryRSS: 1024, ThreadCount: 3, Connections: 7}}}
	samples := map[int]ports.ProcSample{42: {MemoryRSS: 1024}}
	if got := testing.AllocsPerRun(100, func() { _ = refreshStats(rows, samples) }); got != 0 {
		t.Fatalf("unchanged stats allocated %g times", got)
	}
	samples = map[int]ports.ProcSample{99: {MemoryRSS: 2048}}
	if got := testing.AllocsPerRun(100, func() { _ = refreshStats(rows, samples) }); got != 0 {
		t.Fatalf("unmatched sample allocated %g times", got)
	}
}

func TestStatsRefreshPreservesSnapshots(t *testing.T) {
	rows := []state.Port{
		{PID: 42, Stats: &state.Stats{MemoryRSS: 1024, ThreadCount: 3, Connections: 7}},
		{PID: 43, Stats: &state.Stats{MemoryRSS: 2048}},
	}
	next := refreshStats(rows, map[int]ports.ProcSample{42: {MemoryRSS: 8192}, 43: {MemoryRSS: 2048}})
	if next[0].Stats == rows[0].Stats || next[1].Stats != rows[1].Stats {
		t.Fatal("copy-on-write must only replace changed stats")
	}
	if rows[0].Stats.MemoryRSS != 1024 || next[0].Stats.MemoryRSS != 8192 || next[0].Stats.ThreadCount != 3 || next[0].Stats.Connections != 7 {
		t.Fatal("input changed or scan-only fields lost")
	}
	if &next[0] == &rows[0] {
		t.Fatal("changed result aliases the old row array")
	}
}

func TestStatsSingleSnapshotSurvivesRepublish(t *testing.T) {
	r := newStatsRig(t)
	if _, err := r.loop.Rescan(Include{Stats: true}); err != nil {
		t.Fatal(err)
	}
	before := r.loop.Cached()
	saved := *before.Ports[0].Stats
	r.loop.sampleStats(Include{Stats: true})
	sampled := r.loop.Cached()
	if err := r.loop.Republish(); err != nil {
		t.Fatal(err)
	}
	after := r.loop.Cached()
	if !reflect.DeepEqual(before.Ports[0].Stats, &saved) {
		t.Fatal("old reader's snapshot was mutated")
	}
	if !reflect.DeepEqual(after.Ports[0].Stats, sampled.Ports[0].Stats) {
		t.Fatal("republish lost the sampled stats")
	}
}

// A republication has old OS rows; a completed OS scan has new evidence. Only
// the former is allowed to prefer the published sample over non-nil stats.
func TestStatsMergeRespectsFreshScanAndRuntimeIdentity(t *testing.T) {
	prev := []state.Port{{Port: 4000, PID: 42, Stats: &state.Stats{MemoryRSS: 200}}}
	for _, replace := range []bool{false, true} {
		next := []state.Port{{Port: 4000, PID: 42, Stats: &state.Stats{MemoryRSS: 100}}}
		mergeStats(prev, next, replace)
		want := int64(100)
		if replace {
			want = 200
		}
		if next[0].Stats.MemoryRSS != want {
			t.Fatalf("replace=%v: got %d, want %d", replace, next[0].Stats.MemoryRSS, want)
		}
	}
	for _, mutate := range []func(*state.Port){
		func(p *state.Port) { p.PID++ },
		func(p *state.Port) { s := "new-start"; p.StartedAt = &s },
		func(p *state.Port) { p.Run = &state.Run{ID: "new-run"} },
		func(p *state.Port) { p.Docker = &state.Docker{Container: "new-container"} },
	} {
		next := []state.Port{{Port: 4000, PID: 42, Stats: &state.Stats{MemoryRSS: 100}}}
		mutate(&next[0])
		mergeStats(prev, next, true)
		if next[0].Stats.MemoryRSS != 100 {
			t.Fatal("sample crossed a runtime identity boundary")
		}
	}
}
