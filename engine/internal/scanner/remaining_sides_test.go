package scanner

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

func TestRebuildingTheIndexKeepsIdentitiesAlreadyRecorded(t *testing.T) {
	l := New(Options{})
	l.ensureIndex()
	cfg := &groups.Config{Dir: t.TempDir(), Name: "demo", Path: "oberth.yaml"}
	ledger := &identityLedger{}
	if !l.recordIdentity(ledger, "token", cfg) || len(ledger.got) != 1 {
		t.Fatalf("first record = %+v", ledger.got)
	}
	l.ensureIndex()
	if !l.recordIdentity(ledger, "token", cfg) || len(ledger.got) != 1 {
		t.Fatalf("records = %+v, rebuilding the index wrote the same identity again", ledger.got)
	}
}

func TestRenamedGroupIsWrittenAgain(t *testing.T) {
	l := New(Options{})
	l.ensureIndex()
	cfg := &groups.Config{Dir: t.TempDir(), Name: "demo", Path: "oberth.yaml"}
	ledger := &identityLedger{}
	if !l.recordIdentity(ledger, "token", cfg) {
		t.Fatal("first record failed")
	}
	cfg.Name = "renamed"
	if !l.recordIdentity(ledger, "token", cfg) || len(ledger.got) != 2 || ledger.got[1].Group != "renamed" {
		t.Fatalf("records = %+v, a renamed group was not written", ledger.got)
	}
}

func TestNilConfigAndNilStoreAreNotRecorded(t *testing.T) {
	l := New(Options{})
	l.ensureIndex()
	ledger := &identityLedger{}
	l.recordIdentities(ledger, []*groups.Config{nil, {Dir: ""}})
	if len(ledger.got) != 0 {
		t.Fatalf("records = %+v, a missing config was written", ledger.got)
	}
	l.recordIdentityRoot(nil, t.TempDir())
	if len(ledger.got) != 0 {
		t.Fatal("a nil store recorded an identity")
	}
}

func TestVanishedRootIsForgottenOnce(t *testing.T) {
	dir := t.TempDir()
	missing := dir + "/gone"
	st := &countingRoots{roots: []string{missing}}
	l := loopWith(st)
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatal(err)
	}
	if st.removes != 1 {
		t.Fatalf("removes = %d, a vanished root was forgotten more than once", st.removes)
	}
}

type countingRoots struct {
	identityLedger
	roots     []string
	rootsRead int
	removes   int
	adds      int
}

func (s *countingRoots) Roots() ([]string, error) {
	s.rootsRead++
	return append([]string(nil), s.roots...), nil
}
func (s *countingRoots) RemoveRoot(string) error { s.removes++; return nil }
func (s *countingRoots) AddRoot(string) error    { s.adds++; return nil }

func TestRememberedRootIsNotAddedTwice(t *testing.T) {
	l := New(Options{})
	l.ensureIndex()
	dir := t.TempDir()
	writeConfig(t, dir, "name: once\n")
	l.attr.index.Observe(dir)
	st := &countingRoots{}
	l.rememberRoots(st, l.attr.index)
	l.rememberRoots(st, l.attr.index)
	if st.adds != 1 {
		t.Fatalf("adds = %d, an already remembered root was added again", st.adds)
	}
}

func TestHistoryWriteFailureIsLoggedAndNotTreatedAsStored(t *testing.T) {
	warns := &warnCount{}
	ledger := &historyLedger{}
	l := New(Options{Store: ledger, Logger: slog.New(warns)})
	port := state.Port{Port: 9, PID: 1}
	l.record([]state.Event{{Kind: storeEventUp, Port: &port}})
	if warns.n != 0 || len(ledger.rows) != 1 {
		t.Fatalf("warns=%d rows=%d, a successful history write was treated as a failure", warns.n, len(ledger.rows))
	}
}

const storeEventUp = "port_up"

type failHistory struct{ historyLedger }

func (failHistory) AppendBatch([]store.HistoryEvent) error { return errors.New("disk full") }

func TestHistoryWriteFailureWarns(t *testing.T) {
	warns := &warnCount{}
	l := New(Options{Store: &failHistory{}, Logger: slog.New(warns)})
	port := state.Port{Port: 9, PID: 1}
	l.record([]state.Event{{Kind: storeEventUp, Port: &port}})
	if warns.n != 1 {
		t.Fatalf("warns = %d, a failed history write was silent", warns.n)
	}
}

func TestStoppedCommandIsNotProbedAndARunningOneIs(t *testing.T) {
	dir := t.TempDir()
	health := groups.ParseHealth("command:true")
	stopped := &state.Service{Name: "down", Health: strPtr("command:true")}
	if _, ok := commandHealthTarget(state.Group{RootDir: &dir}, stopped, health, nil, 0, 0); ok {
		t.Fatal("a stopped command check was built")
	}
	running := &state.Service{Name: "up", Running: true}
	target, ok := commandHealthTarget(state.Group{RootDir: &dir}, running, health, nil, 0, 1)
	if !ok || target.service != 1 || target.group != 0 {
		t.Fatalf("target=%+v ok=%v, a running command check was not built", target, ok)
	}
	if _, ok := commandHealthTarget(state.Group{}, running, health, nil, 0, 0); ok {
		t.Fatal("a command check without a project root was built")
	}
}

func strPtr(s string) *string { return &s }

func TestCommandIndexEqualToTheLengthIsOutOfRange(t *testing.T) {
	gg := []state.Group{{Services: []state.Service{{Name: "only"}}}}
	ok := state.Health{Status: state.HealthOK}
	round := &healthRound{probed: []bool{true, true}, results: []state.Health{ok, ok}}
	applyCommandHealth(gg, []healthTarget{
		{kind: groups.HealthCommand, group: len(gg), service: 0},
		{kind: groups.HealthCommand, group: 0, service: len(gg[0].Services)},
	}, round)
	if gg[0].Services[0].HealthStatus != nil {
		t.Fatalf("health = %+v, an index equal to the length was written", gg[0].Services[0].HealthStatus)
	}
}

func TestProbedPortCheckIsNotWrittenAsACommand(t *testing.T) {
	gg := []state.Group{{Services: []state.Service{{Name: "api"}}}}
	round := &healthRound{probed: []bool{true}, results: []state.Health{{Status: state.HealthOK}}}
	applyCommandHealth(gg, []healthTarget{{
		kind: groups.HealthHTTP, group: 0, service: 0,
	}}, round)
	if gg[0].Services[0].HealthStatus != nil {
		t.Fatal("a port check was written onto the service as a command verdict")
	}
}

func TestRecordedProbeDropsADepartedCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	round := &healthRound{results: make([]state.Health, 1), probed: make([]bool, 1)}
	recordHealthProbe(ctx, func(context.Context, string, int, string, time.Duration) ports.HealthResult {
		calls++
		return ports.HealthResult{Status: "healthy"}
	}, healthTarget{kind: groups.HealthHTTP, host: "127.0.0.1", port: 9}, time.Time{}, round, 0)
	if calls != 0 || round.probed[0] {
		t.Fatalf("calls=%d probed=%v, a departed caller was probed", calls, round.probed[0])
	}
}

func TestMachineOnlyGroupStillNamesItsPort(t *testing.T) {
	got := groupPortMap(state.Group{Machine: []state.MachineRef{{Name: "db", Port: 5432}}})
	if got["db"] != 5432 {
		t.Fatalf("ports = %v, want db=5432", got)
	}
}

func TestIdleDaemonDoesNotScanWhenTheCallerHasLeft(t *testing.T) {
	l := New(Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, scanNow, finished := l.waitForDemand(ctx)
	if scanNow || !finished {
		t.Fatalf("scanNow=%v finished=%v, an idle daemon started a scan for a caller who had left", scanNow, finished)
	}
}

func TestWakeWhileParkedRereadsDemand(t *testing.T) {
	l := New(Options{Demand: noSubscribers})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Wake()
	_, scanNow, finished := l.waitForDemand(ctx)
	if scanNow || finished {
		t.Fatalf("scanNow=%v finished=%v, a wake while parked scanned instead of re-reading demand", scanNow, finished)
	}
}

func TestDueAndNotYetDue(t *testing.T) {
	frozen := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	now := frozen
	scans := 0
	saw := make(chan struct{})
	l := New(Options{
		Now:          func() time.Time { return now },
		ScanInterval: MinScanInterval,
		Scan: func(Include) ([]ports.ListeningPort, error) {
			scans++
			close(saw)
			return []ports.ListeningPort{{Port: 9, PID: 1}}, nil
		},
	})
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.waitTick(ctx, timer, Include{})
	<-saw
	if scans != 1 {
		t.Fatalf("scans=%d, a scan that was due did not run", scans)
	}
	cancel()

	now = frozen
	scans = 0
	l = New(Options{
		Now:          func() time.Time { return now },
		ScanInterval: MinScanInterval,
		Scan: func(Include) ([]ports.ListeningPort, error) {
			scans++
			return []ports.ListeningPort{{Port: 9, PID: 1}}, nil
		},
	})
	if _, _, _, err := l.scanLocked(Include{}, Include{}, false); err != nil {
		t.Fatal(err)
	}
	now = frozen.Add(MinScanInterval - time.Nanosecond)
	early, stop := context.WithCancel(context.Background())
	stop()
	other := time.NewTimer(time.Hour)
	defer other.Stop()
	l.waitTick(early, other, Include{})
	if scans != 1 {
		t.Fatalf("scans=%d, a scan one nanosecond early ran", scans)
	}
}

func TestCancelledCollectionDoesNotCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	warns := &warnCount{}
	l := New(Options{
		Logger: slog.New(warns),
		ScanContext: func(context.Context, Include) ([]ports.ListeningPort, error) {
			cancel()
			return nil, &ports.ObservationError{
				Partial: true, Code: "blind", Detail: "half", Hint: "retry",
				Rows: []ports.ListeningPort{{Port: 9, PID: 1, Process: "app"}},
			}
		},
	})
	_, _, changed, err := l.scanLockedContext(ctx, Include{}, Include{}, true)
	if changed || !errors.Is(err, context.Canceled) || warns.n != 0 || l.Cached().Seq != 0 {
		t.Fatalf("changed=%v err=%v warns=%d seq=%d, a cancelled collection was published as a partial scan", changed, err, warns.n, l.Cached().Seq)
	}
}

func TestPartialRowsStayPartial(t *testing.T) {
	warns := &warnCount{}
	l := New(Options{
		Logger: slog.New(warns),
		Scan: func(Include) ([]ports.ListeningPort, error) {
			return nil, &ports.ObservationError{
				Partial: true, Code: "blind", Detail: "half the table", Hint: "retry",
				Rows: []ports.ListeningPort{{Port: 9, PID: 1, Process: "app"}},
			}
		},
	})
	snap, _, _, err := l.scanLocked(Include{}, Include{}, true)
	if err != nil || warns.n != 1 || len(snap.Ports) != 1 || snap.Observation == nil || snap.Observation.State != state.ObservationPartial {
		t.Fatalf("err=%v warns=%d obs=%+v ports=%d", err, warns.n, snap.Observation, len(snap.Ports))
	}
}

func TestEmptyCacheRepublishScans(t *testing.T) {
	scans := 0
	l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) {
		scans++
		return []ports.ListeningPort{{Port: 9, PID: 1, Process: "app"}}, nil
	}})
	l.mu.Lock()
	l.haveSnap = true
	l.lastPorts = nil
	l.mu.Unlock()
	lock(l.orderGate)
	if err := l.republishHeld(context.Background()); err != nil {
		t.Fatal(err)
	}
	if scans != 1 || len(l.Cached().Ports) != 1 {
		t.Fatalf("scans=%d cached=%d, an empty port list did not observe the machine", scans, len(l.Cached().Ports))
	}
}

func TestParentDeadlineAtTheBudgetIsReused(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	child, childCancel := observationContext(parent)
	childCancel()
	if parent.Err() != nil {
		t.Fatal("a nested observation cancelled the caller's shorter deadline")
	}
	select {
	case <-parent.Done():
	case <-time.After(time.Second):
		t.Fatal("parent deadline was replaced")
	}
	_ = child
}

func TestServicePIDOfOneIsSampled(t *testing.T) {
	groups := []state.Group{{Services: []state.Service{
		{Name: "one", Running: true, PID: intPtr(1)},
	}}}
	got := serviceStatsPIDs(groups)
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("pids = %v, want 1", got)
	}
	stopped := serviceStatsPIDs([]state.Group{{Services: []state.Service{
		{Name: "down", PID: intPtr(4)},
	}}})
	if len(stopped) != 0 {
		t.Fatalf("pids = %v, a stopped service was sampled", stopped)
	}
}
