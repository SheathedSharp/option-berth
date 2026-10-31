package scanner

import (
	"context"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func TestFillServiceStatsSamplesAPortlessWorker(t *testing.T) {
	workerPID := 4242
	groups := []state.Group{{
		Name: "demo",
		Services: []state.Service{
			{Name: "worker", Running: true, PID: &workerPID},
			{Name: "api", Running: true, PortActual: intPtr(3000), PID: intPtr(3001)},
			{Name: "stopped", PID: intPtr(5000)},
		},
	}}

	var sampled []int
	FillServiceStatsContext(context.Background(), groups, func(pids []int) map[int]ports.ProcSample {
		sampled = append(sampled, pids...)
		return map[int]ports.ProcSample{workerPID: {
			CPUPercent: 12.5, MemoryRSS: 1 << 20, ThreadCount: 3, Uptime: "1m", State: "running",
		}}
	})

	if len(sampled) != 1 || sampled[0] != workerPID {
		t.Fatalf("sampled pids = %v, want only the portless worker", sampled)
	}
	svc := groups[0].Services[0]
	if svc.Stats == nil || svc.Stats.CPUPercent != 12.5 || svc.Stats.MemoryRSS != 1<<20 {
		t.Fatalf("worker stats = %+v, want the sample", svc.Stats)
	}
	if groups[0].Services[1].Stats != nil {
		t.Fatal("a listener service was sampled again instead of using its port row")
	}
	if groups[0].Services[2].Stats != nil {
		t.Fatal("a stopped service received a sample")
	}
}

func TestFillServiceStatsRejectsARecycledPID(t *testing.T) {
	pid := 4242
	started := "2026-10-31T10:00:00Z"
	groups := []state.Group{{
		Name: "demo",
		Services: []state.Service{{
			Name: "worker", Running: true, PID: &pid, StartedAt: &started,
		}},
	}}

	FillServiceStatsContext(context.Background(), groups, func([]int) map[int]ports.ProcSample {
		return map[int]ports.ProcSample{pid: {
			CPUPercent: 99, MemoryRSS: 1 << 20,
			// The run started at 10:00; this process is a different lifetime.
			StartedAt: "2026-10-31T11:00:00Z",
		}}
	})
	if groups[0].Services[0].Stats != nil {
		t.Fatal("a recycled pid was accepted as the run's process")
	}
}

func TestRefreshGroupStatsKeepsPortAndServiceRowsInStep(t *testing.T) {
	port := 3000
	workerPID := 4242
	groups := []state.Group{{
		Name: "demo",
		Services: []state.Service{
			{Name: "api", Running: true, PortActual: &port},
			{Name: "worker", Running: true, PID: &workerPID},
		},
	}}
	rows := []state.Port{{Port: port, Stats: &state.Stats{CPUPercent: 5, MemoryRSS: 2 << 20}}}

	next := refreshGroupStats(groups, rows, map[int]ports.ProcSample{
		workerPID: {CPUPercent: 7, MemoryRSS: 3 << 20},
	})

	if next[0].Services[0].Stats == nil || next[0].Services[0].Stats.CPUPercent != 5 {
		t.Fatalf("api stats = %+v, want the port row's sample", next[0].Services[0].Stats)
	}
	if next[0].Services[1].Stats == nil || next[0].Services[1].Stats.CPUPercent != 7 {
		t.Fatalf("worker stats = %+v, want its own sample", next[0].Services[1].Stats)
	}
	if next[0].RunningServices != 2 || next[0].Resources == nil ||
		next[0].Resources.CPUPercent != 12 || next[0].Resources.MemoryRSS != 5<<20 {
		t.Fatalf("group totals = %d running / %+v, want 2 and 12 percent / 5MiB",
			next[0].RunningServices, next[0].Resources)
	}
	if groups[0].Services[0].Stats != nil {
		t.Fatal("the input snapshot was mutated instead of copied")
	}
}

func TestRefreshGroupStatsDoesNotInventAnEmptySample(t *testing.T) {
	workerPID := 4242
	groups := []state.Group{{
		Name:     "demo",
		Services: []state.Service{{Name: "worker", Running: true, PID: &workerPID}},
	}}
	next := refreshGroupStats(groups, nil, nil)
	if next[0].Services[0].Stats != nil || next[0].Resources != nil {
		t.Fatalf("empty sample produced stats %+v / resources %+v", next[0].Services[0].Stats, next[0].Resources)
	}
}

func TestResourceTotalsCountOnlySamples(t *testing.T) {
	group := state.Group{Services: []state.Service{
		{Running: true, Stats: &state.Stats{CPUPercent: 1, MemoryRSS: 10}},
		{Running: true},
		{Running: false, Stats: &state.Stats{CPUPercent: 100, MemoryRSS: 1000}},
	}}
	if got := group.RunningServiceCount(); got != 2 {
		t.Fatalf("running count = %d, want 2", got)
	}
	got := group.ResourceTotals()
	if got == nil || got.Services != 1 || got.CPUPercent != 1 || got.MemoryRSS != 10 {
		t.Fatalf("totals = %+v, want one sampled service", got)
	}
	if empty := (state.Group{}).ResourceTotals(); empty != nil {
		t.Fatalf("no samples produced %+v, want nil", empty)
	}
}

func intPtr(v int) *int { return &v }
