package scanner

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

func TestCadenceTimerStartsTheNextScan(t *testing.T) {
	var scans atomic.Int32
	second := make(chan struct{})
	l := New(Options{
		ScanInterval: MinScanInterval,
		Demand:       oneSubscriber,
		SampleStats:  func([]int) map[int]ports.ProcSample { return nil },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			if scans.Add(1) == 2 {
				close(second)
			}
			return []ports.ListeningPort{{Port: 3000, PID: 7, Process: "app"}}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		l.Run(ctx)
	}()
	select {
	case <-second:
	case <-time.After(3 * time.Second):
		t.Fatal("the cadence timer did not start another scan")
	}
	cancel()
	<-done
}

func TestSameVerdictIsNotAChangeAndADifferentHintIs(t *testing.T) {
	verdict := state.Observation{State: "observable", Code: "ok", Source: "ss", Detail: "partial owners", Hint: "use lsof"}
	prev := state.Snapshot{Observation: &verdict}
	same := state.Observation{State: "observable", Code: "ok", Source: "ss", Detail: "partial owners", Hint: "use lsof"}
	if snapshotChanged(prev, state.Snapshot{Observation: &same}, false) {
		t.Fatal("a re-checked verdict with the same facts was treated as a new snapshot")
	}
	changed := same
	changed.Hint = "different"
	if !snapshotChanged(prev, state.Snapshot{Observation: &changed}, false) {
		t.Fatal("a different hint was kept off the published diff")
	}
}

func TestReadyTimeoutIsNotRepeatedWhenTheGroupChangesForAnotherReason(t *testing.T) {
	root := "/code/example-worker"
	exit := state.ServiceExit{Code: 1, Reason: "ready_timeout", At: "now", RunID: "run-1"}
	before := state.Group{
		Name: "example-worker", Repo: "example-worker", RootDir: &root,
		Services: []state.Service{{Name: "api", LastExit: &exit}},
	}
	after := before
	after.Services = []state.Service{{Name: "api", Running: true, LastExit: &exit}}
	events := deriveEvents(
		state.Snapshot{Seq: 4, Groups: []state.Group{before}},
		state.Snapshot{Seq: 5, At: "later", Groups: []state.Group{after}},
		"later",
	)
	for _, ev := range events {
		if ev.Kind == "ready_timeout" {
			t.Fatalf("events = %+v, the same exit must not be notified again", events)
		}
	}
	other := exit
	other.RunID = "run-2"
	after.Services = []state.Service{{Name: "api", LastExit: &other}}
	events = deriveEvents(
		state.Snapshot{Seq: 5, Groups: []state.Group{before}},
		state.Snapshot{Seq: 6, At: "later", Groups: []state.Group{after}},
		"later",
	)
	found := false
	for _, ev := range events {
		if ev.Kind == "ready_timeout" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %+v, want a ready_timeout for the new run", events)
	}
}

type historyLedger struct {
	identityLedger
	rows []store.HistoryEvent
}

func (h *historyLedger) AppendBatch(events []store.HistoryEvent) error {
	h.rows = append(h.rows, events...)
	return nil
}

func TestHistoryKeepsOnlyPortUpDownAndRestart(t *testing.T) {
	port := state.Port{Port: 3000, PID: 7}
	ledger := &historyLedger{}
	l := New(Options{Store: ledger})
	l.record([]state.Event{
		{Kind: store.EventPortUp, Port: &port},
		{Kind: "health_changed", Port: &port},
		{Kind: "scan_error"},
	})
	if len(ledger.rows) != 1 || ledger.rows[0].Kind != store.EventPortUp || ledger.rows[0].Port != 3000 {
		t.Fatalf("history = %+v, want only the port_up row", ledger.rows)
	}
}

func TestCancelledHealthRoundDoesNotProbe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	probes := 0
	_, err := runHealthRound(ctx, []healthTarget{{
		kind: groups.HealthHTTP, host: "127.0.0.1", port: 9, path: "/",
	}}, func(context.Context, string, int, string, time.Duration) ports.HealthResult {
		probes++
		return ports.HealthResult{Status: "healthy"}
	}, time.Second)
	if err == nil || probes != 0 {
		t.Fatalf("err=%v probes=%d, want the caller's cancellation and no probe", err, probes)
	}
}

func TestHealthIsDueAgainOnlyAfterTheCadence(t *testing.T) {
	frozen := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	now := frozen
	l := New(Options{Now: func() time.Time { return now }})
	if !l.healthDue() {
		t.Fatal("a loop that has never probed skipped health")
	}
	l.mu.Lock()
	l.lastHealthAt = now
	l.mu.Unlock()
	if l.healthDue() {
		t.Fatal("a probe just taken was due again")
	}
	now = frozen.Add(HealthCadence)
	if !l.healthDue() {
		t.Fatal("health stayed idle after the cadence")
	}
}

type keptRegistry struct{}

func (keptRegistry) Run(state.Port) (state.Run, bool) {
	return state.Run{ID: "kept"}, true
}

func TestNilRunRegistryDoesNotClearTheInstalledOne(t *testing.T) {
	l := New(Options{})
	l.SetRuns(func() groups.Registry { return keptRegistry{} })
	l.SetRuns(nil)
	run, ok := l.registry().Run(state.Port{})
	if !ok || run.ID != "kept" {
		t.Fatalf("registry after nil SetRuns = %+v ok=%v", run, ok)
	}
}

func TestStatsDiffNoticesShapeAndIgnoresIdenticalNumbers(t *testing.T) {
	same := state.Stats{MemoryRSS: 10}
	if statsChanged(
		state.Snapshot{Ports: []state.Port{{Stats: &same}}},
		state.Snapshot{Ports: []state.Port{{Stats: &state.Stats{MemoryRSS: 10}}}},
	) {
		t.Fatal("identical port stats were a change")
	}
	if statsChanged(
		state.Snapshot{Ports: []state.Port{{}}},
		state.Snapshot{Ports: []state.Port{{}}},
	) {
		t.Fatal("two rows with no stats were a change")
	}
	if !statsChanged(
		state.Snapshot{Ports: []state.Port{{}}},
		state.Snapshot{Ports: []state.Port{{}, {}}},
	) {
		t.Fatal("a gained port was not a stats change")
	}
	if !statsChanged(
		state.Snapshot{Ports: []state.Port{{}}},
		state.Snapshot{Ports: []state.Port{{Stats: &same}}},
	) {
		t.Fatal("stats appearing on a row was not a change")
	}
	resources := state.Resources{CPUPercent: 1}
	if !statsChanged(
		state.Snapshot{Groups: []state.Group{{}}},
		state.Snapshot{Groups: []state.Group{{Resources: &resources}}},
	) {
		t.Fatal("a group resource total appearing was not a change")
	}
}

func TestUnparseableProcessStartDoesNotRejectTheSample(t *testing.T) {
	pid := 4242
	started := "not-a-timestamp"
	groups := []state.Group{{Services: []state.Service{{
		Name: "worker", Running: true, PID: &pid, StartedAt: &started,
	}}}}
	FillServiceStatsContext(context.Background(), groups, func([]int) map[int]ports.ProcSample {
		return map[int]ports.ProcSample{pid: {MemoryRSS: 10, StartedAt: "also-not"}}
	})
	if groups[0].Services[0].Stats == nil || groups[0].Services[0].Stats.MemoryRSS != 10 {
		t.Fatalf("stats = %+v, an unreadable start is unknown and the sample stays", groups[0].Services[0].Stats)
	}
}

func TestStatsSampleWithoutASnapshotPublishesNothing(t *testing.T) {
	published := 0
	l := New(Options{Publish: func(state.Snapshot, state.Snapshot, []state.Event) { published++ }})
	l.sampleStats(Include{Stats: true})
	if published != 0 {
		t.Fatalf("published %d times with no snapshot", published)
	}
}
