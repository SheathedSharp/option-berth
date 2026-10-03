package scanner

import (
	"context"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestStatsRejectsReusedPIDBeforeNextFullScan(t *testing.T) {
	at := "2026-01-01T00:00:00Z"
	rows := []state.Port{{Host: state.LocalhostName, Port: 8080, PID: 42, StartedAt: &at, Stats: &state.Stats{MemoryRSS: 100}}}
	samples := map[int]ports.ProcSample{42: {StartedAt: "2026-01-02T00:00:00Z", MemoryRSS: 900}}
	for _, rebase := range []bool{false, true} {
		out := refreshStatsFrom(rows, samples, rows, rebase)
		if out[0].Stats.MemoryRSS != 100 {
			t.Fatalf("rebase=%v: statistics crossed observed process birth identity", rebase)
		}
	}
}

func TestStatsObservationIdentityCompatibility(t *testing.T) {
	at := "2026-01-01T00:00:00Z"
	for _, sampleAt := range []string{"2026-01-01T00:00:00Z", "2026-01-01T09:00:00+09:00", ""} {
		rows := []state.Port{{Host: state.LocalhostName, PID: 42, StartedAt: &at, Stats: &state.Stats{MemoryRSS: 100}}}
		out := refreshStats(rows, map[int]ports.ProcSample{42: {StartedAt: sampleAt, MemoryRSS: 900}})
		if out[0].Stats.MemoryRSS != 900 || rows[0].Stats.MemoryRSS != 100 {
			t.Fatalf("known same/missing observation or immutable snapshot changed: %s", sampleAt)
		}
	}
}

func TestStatsCannotPatchRemotePIDWithLocalSample(t *testing.T) {
	rows := []state.Port{{Host: "other-host", PID: 42, Stats: &state.Stats{MemoryRSS: 100}}}
	if out := refreshStats(rows, map[int]ports.ProcSample{42: {MemoryRSS: 900}}); out[0].Stats.MemoryRSS != 100 {
		t.Fatal("local sampler patched matching PID on another host")
	}
}

func TestCancelledStatsLoopDoesNotCollectOrPublish(t *testing.T) {
	calls := 0
	l := New(Options{Demand: func() (int, Include) { return 1, Include{Stats: true} }, Scan: func(Include) ([]ports.ListeningPort, error) {
		return []ports.ListeningPort{{Port: 8080, PID: 42, MemoryRSS: 100}}, nil
	}, SampleStats: func([]int) map[int]ports.ProcSample { calls++; return map[int]ports.ProcSample{42: {MemoryRSS: 900}} }})
	l.scanAndPublish(Include{Stats: true})
	before := l.Cached()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l.runStats(ctx)
	if calls != 0 || l.Cached().Seq != before.Seq {
		t.Fatal("already cancelled stats loop collected or published")
	}
}

func TestStatsCancellationDiscardsCompletedSample(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := New(Options{Demand: func() (int, Include) { return 1, Include{Stats: true} }, Scan: func(Include) ([]ports.ListeningPort, error) {
		return []ports.ListeningPort{{Port: 8080, PID: 42, MemoryRSS: 100}}, nil
	}, SampleStats: func([]int) map[int]ports.ProcSample { cancel(); return map[int]ports.ProcSample{42: {MemoryRSS: 900}} }})
	l.scanAndPublish(Include{Stats: true})
	before := l.Cached()
	l.runStats(ctx)
	if l.Cached().Seq != before.Seq || l.Cached().Ports[0].Stats.MemoryRSS != 100 {
		t.Fatal("sample published after caller cancellation")
	}
}
