package scanner

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func TestRepublishKeepsUnconfiguredHealthAndAnUnchangedOnePublishesNothing(t *testing.T) {
	l := New(Options{Scan: fixedRows(ports.ListeningPort{Port: 9, PID: 1, Process: "app"})})
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatal(err)
	}
	seq := l.Cached().Seq
	published := 0
	l.opts.Publish = func(state.Snapshot, state.Snapshot, []state.Event) { published++ }
	if err := l.Republish(); err != nil {
		t.Fatal(err)
	}
	if l.Cached().Seq != seq || published != 0 {
		t.Fatalf("seq %d -> %d published=%d, republishing the same rows published a change", seq, l.Cached().Seq, published)
	}

	health := &state.Health{Status: state.HealthOK}
	l.mu.Lock()
	l.snap.Ports[0].Health = health
	l.mu.Unlock()
	rows := []state.Port{{Port: l.Cached().Ports[0].Port, PID: 1, Process: "app"}}
	next, _, _ := l.commitRepublish(0, Include{}, rows, l.Cached().Groups, l.Cached().Sessions)
	if next.Ports[0].Health == nil || next.Ports[0].Health.Status != state.HealthOK {
		t.Fatalf("health=%+v, republish dropped a health result that was not part of the declaration", next.Ports[0].Health)
	}
}

func TestAFailedReadWithNoSnapshotReturnsTheError(t *testing.T) {
	warns := &warnCount{}
	l := New(Options{
		Logger: slog.New(warns),
		Scan:   func(Include) ([]ports.ListeningPort, error) { return nil, errors.New("down") },
	})
	if _, err := l.Snapshot(Include{}); err == nil || warns.n != 0 {
		t.Fatalf("err=%v warns=%d, a failed read with nothing cached was served as success or logged as a loop failure", err, warns.n)
	}
}

func TestARecentReadinessFailureIsAnErrorNotAFreshSnapshot(t *testing.T) {
	frozen := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	failed := errors.New("down")
	scans := 0
	l := New(Options{
		Now: func() time.Time { return frozen },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			scans++
			return nil, failed
		},
	})
	if _, err := l.SnapshotForReadiness(); !errors.Is(err, failed) {
		t.Fatalf("err=%v, the first readiness read did not return the failure", err)
	}
	_, reused, err := l.reuseIfFresh(0, Include{}, readinessScan)
	if !errors.Is(err, failed) || reused {
		t.Fatalf("reused=%v err=%v, a recent readiness failure was handed out as a fresh snapshot", reused, err)
	}
	if _, err := l.SnapshotForReadiness(); !errors.Is(err, failed) || scans != 1 {
		t.Fatalf("err=%v scans=%d, a recent readiness failure was scanned again or hidden", err, scans)
	}
}

func TestStatsAreNotSampledBeforeAnyScanOrAfterTheCallerLeavesMidSample(t *testing.T) {
	calls := 0
	l := New(Options{SampleStats: func([]int) map[int]ports.ProcSample {
		calls++
		return map[int]ports.ProcSample{7: {MemoryRSS: 99}}
	}})
	l.sampleStatsContext(context.Background(), Include{Stats: true})
	if calls != 0 || l.Cached().Seq != 0 {
		t.Fatalf("calls=%d seq=%d, stats were sampled before any scan", calls, l.Cached().Seq)
	}

	if _, _, _, err := l.scanLocked(Include{}, Include{}, false); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	l.snap.Ports = []state.Port{{Port: 9, PID: 7, Stats: &state.Stats{MemoryRSS: 1}}}
	l.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	l.opts.SampleStats = func([]int) map[int]ports.ProcSample {
		calls++
		cancel()
		return map[int]ports.ProcSample{7: {MemoryRSS: 99}}
	}
	l.sampleStatsContext(ctx, Include{Stats: true})
	if l.Cached().Ports[0].Stats.MemoryRSS != 1 {
		t.Fatalf("stats=%+v, a sample whose caller left was published", l.Cached().Ports[0].Stats)
	}
}

func TestARepublishBetweenSampleAndPublishDoesNotPatchTheNewGroups(t *testing.T) {
	l := New(Options{})
	pid := 7
	l.mu.Lock()
	l.haveSnap = true
	l.snap = state.Snapshot{
		Seq: 3,
		Ports: []state.Port{{
			Port: 9, PID: pid, Stats: &state.Stats{MemoryRSS: 1},
		}},
		Groups: []state.Group{{Services: []state.Service{{
			Name: "worker", Running: true, PID: &pid,
		}}}},
	}
	l.committedSeq = 3
	l.statsScanSeq = 1
	l.seq = 3
	l.mu.Unlock()

	seqMoved := l.captureForStatsTest()
	seqMoved.snap.Seq = 3
	l.mu.Lock()
	l.snap.Seq = 4
	l.seq = 4
	l.mu.Unlock()
	if _, _, publish := l.mergeStatsSample(seqMoved, map[int]ports.ProcSample{7: {MemoryRSS: 9}}); !publish {
		t.Fatal("a stats sample that still matches the scan generation was dropped")
	}
	if got := l.Cached().Groups[0].Services[0].Stats; got != nil {
		t.Fatalf("group stats=%+v, a sample taken before a republish patched the new groups", got)
	}

	l.mu.Lock()
	l.snap.Seq = 4
	l.committedSeq = 4
	l.snap.Groups[0].Services[0].Stats = nil
	l.snap.Ports[0].Stats = &state.Stats{MemoryRSS: 1}
	l.mu.Unlock()
	genMoved := l.captureForStatsTest()
	genMoved.scan = 3
	if _, _, publish := l.mergeStatsSample(genMoved, map[int]ports.ProcSample{7: {MemoryRSS: 8}}); !publish {
		t.Fatal("a stats sample against an older scan generation was dropped")
	}
	if got := l.Cached().Groups[0].Services[0].Stats; got != nil {
		t.Fatalf("group stats=%+v, a sample from an older scan generation patched the new groups", got)
	}
}

func (l *Loop) captureForStatsTest() statsBasis {
	basis, ok := l.captureStatsBasis()
	if !ok {
		panic("stats basis missing")
	}
	return basis
}

func TestProcessOneIsSampledAndZeroIsNot(t *testing.T) {
	var sampled []int
	groups := []state.Group{{Services: []state.Service{
		{Name: "one", Running: true, PID: intPtr(1)},
		{Name: "zero", Running: true, PID: intPtr(0)},
	}}}
	FillServiceStatsContext(context.Background(), groups, func(pids []int) map[int]ports.ProcSample {
		sampled = append([]int(nil), pids...)
		return map[int]ports.ProcSample{1: {MemoryRSS: 4}}
	})
	if len(sampled) != 1 || sampled[0] != 1 || groups[0].Services[0].Stats == nil || groups[0].Services[1].Stats != nil {
		t.Fatalf("sampled=%v stats=%+v %+v", sampled, groups[0].Services[0].Stats, groups[0].Services[1].Stats)
	}

	idle := []state.Group{{Services: []state.Service{{Name: "down"}}}}
	called := false
	FillServiceStatsContext(context.Background(), idle, func([]int) map[int]ports.ProcSample {
		called = true
		return map[int]ports.ProcSample{1: {MemoryRSS: 1}}
	})
	if called {
		t.Fatal("a group with nobody to sample still called the sampler")
	}

	ctx, cancel := context.WithCancel(context.Background())
	left := []state.Group{{Services: []state.Service{{Name: "one", Running: true, PID: intPtr(1)}}}}
	FillServiceStatsContext(ctx, left, func([]int) map[int]ports.ProcSample {
		cancel()
		return map[int]ports.ProcSample{1: {MemoryRSS: 4}}
	})
	if left[0].Services[0].Stats != nil {
		t.Fatal("a sample whose caller left was written onto the service")
	}
}

func TestAnEmptyProcessSampleDoesNotCopyListenerStats(t *testing.T) {
	port := 9
	groups := []state.Group{{Services: []state.Service{{
		Name: "api", Running: true, PortActual: &port,
	}}}}
	out := refreshGroupStats(groups, []state.Port{{
		Port: port, Stats: &state.Stats{MemoryRSS: 5},
	}}, map[int]ports.ProcSample{})
	if out[0].Services[0].Stats != nil {
		t.Fatalf("stats=%+v, an empty process sample still copied the listener", out[0].Services[0].Stats)
	}
}

func TestAnIdenticalServiceSampleKeepsTheStatsPointer(t *testing.T) {
	current := &state.Stats{MemoryRSS: 5}
	services := []state.Service{{Name: "api", Running: true, PID: intPtr(7), Stats: current}}
	out, changed := refreshServiceStats(services, nil, map[int]ports.ProcSample{
		7: {MemoryRSS: 5},
	})
	if changed || out[0].Stats != current {
		t.Fatal("an identical service sample replaced the stats object")
	}
	untouched := []state.Service{{Name: "api", Running: true, PID: intPtr(8), Stats: current}}
	out, changed = refreshServiceStats(untouched, nil, map[int]ports.ProcSample{})
	if changed || out[0].Stats != current {
		t.Fatal("a service with no sample lost its stats")
	}
}

func TestLocalProcessOneIsSampledAndADockerRowIsNot(t *testing.T) {
	rows := []state.Port{
		{PID: 1, Stats: &state.Stats{MemoryRSS: 1}},
		{PID: 0, Stats: &state.Stats{MemoryRSS: 1}},
		{Type: state.TypeDocker, PID: 3, Stats: &state.Stats{MemoryRSS: 1}},
		{PID: 4},
	}
	got := statsPIDs(rows)
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("pids=%v, want only the local process 1", got)
	}
	merged := mergePIDs([]int{1, 0, 1}, []int{-1})
	if len(merged) != 1 || merged[0] != 1 {
		t.Fatalf("merged=%v, process 1 was dropped or sampled twice", merged)
	}
	next := refreshStats(rows, map[int]ports.ProcSample{
		1: {MemoryRSS: 1, ThreadCount: 1},
		3: {MemoryRSS: 9},
		4: {MemoryRSS: 9},
	})
	if next[0].Stats.ThreadCount != 1 || next[2].Stats.MemoryRSS != 1 || next[3].Stats != nil {
		t.Fatalf("rows=%+v %+v %+v", next[0].Stats, next[2].Stats, next[3].Stats)
	}
}

func TestAPlainGroupChangeDoesNotEmitAReadyTimeout(t *testing.T) {
	root := "/code/example"
	events := deriveEvents(
		state.Snapshot{Groups: []state.Group{{
			Name: "example", Repo: "example", RootDir: &root,
			Services: []state.Service{{Name: "api"}},
		}}},
		state.Snapshot{Seq: 2, Groups: []state.Group{{
			Name: "example", Repo: "example", RootDir: &root,
			Services: []state.Service{{Name: "api", Running: true}},
		}}},
		"now",
	)
	for _, ev := range events {
		if ev.Kind == "ready_timeout" {
			t.Fatalf("events=%+v, a service with no ready timeout emitted one", events)
		}
	}
	exit := &state.ServiceExit{Reason: "ready_timeout"}
	if !sameServiceExit(nil, nil) || sameServiceExit(nil, exit) || sameServiceExit(exit, nil) {
		t.Fatal("a missing exit was treated as the same exit, or two missing exits as different")
	}
}

func TestPIDOneIsTheSameRuntime(t *testing.T) {
	a := state.Port{PID: 1}
	b := state.Port{PID: 1}
	if !sameRuntime(a, b) {
		t.Fatal("process 1 was treated as having no runtime")
	}
}

func TestSuccessfulStatsEnrichmentKeepsTheListeners(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	l := New(Options{})
	rows, _, err := l.osScanSourceContext(context.Background(), Include{Stats: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("a listener this process opened was dropped after stats enrichment")
	}
}
