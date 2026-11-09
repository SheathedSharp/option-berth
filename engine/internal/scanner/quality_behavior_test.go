package scanner

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// A portless running service is sampled on its own. A listener's process is
// already on the port row, a stopped service has nothing to sample, and a
// missing or non-positive pid is not a process.
func TestPortlessRunningServiceIsTheOnlyExtraSample(t *testing.T) {
	port := 3000
	groups := []state.Group{{Services: []state.Service{
		{Name: "worker", Running: true, PID: intPtr(4242)},
		{Name: "api", Running: true, PortActual: &port, PID: intPtr(3001)},
		{Name: "stopped", PID: intPtr(9)},
		{Name: "blank", Running: true},
		{Name: "zero", Running: true, PID: intPtr(0)},
	}}}
	got := serviceStatsPIDs(groups)
	if len(got) != 1 || got[0] != 4242 {
		t.Fatalf("sampled pids = %v, want only 4242", got)
	}
}

// Group resource totals can stay put while one service's numbers move. That
// is still a change a subscriber must see. The same numbers, and only a
// longer service list, are the other two answers.
func TestServiceStatsChangeIsVisibleWhenGroupResourcesMatch(t *testing.T) {
	resources := &state.Resources{CPUPercent: 1, MemoryRSS: 10}
	prev := state.Snapshot{Groups: []state.Group{{
		Resources: resources,
		Services:  []state.Service{{Name: "worker", Stats: &state.Stats{MemoryRSS: 1}}},
	}}}
	changed := state.Snapshot{Groups: []state.Group{{
		Resources: resources,
		Services:  []state.Service{{Name: "worker", Stats: &state.Stats{MemoryRSS: 2}}},
	}}}
	if !statsChanged(prev, changed) {
		t.Fatal("a service stats change was treated as the same snapshot")
	}
	again := state.Snapshot{Groups: []state.Group{{
		Resources: resources,
		Services:  []state.Service{{Name: "worker", Stats: &state.Stats{MemoryRSS: 2}}},
	}}}
	if statsChanged(changed, again) {
		t.Fatal("identical service stats were treated as a change")
	}
	longer := state.Snapshot{Groups: []state.Group{{
		Resources: resources,
		Services:  []state.Service{{Name: "worker"}, {Name: "extra"}},
	}}}
	if !statsChanged(changed, longer) {
		t.Fatal("a service list that gained a row was ignored")
	}
}

type identityLedger struct {
	fail int
	got  []store.WorktreeIdentity
}

func (s *identityLedger) Roots() ([]string, error)                 { return nil, nil }
func (s *identityLedger) AddRoot(string) error                     { return nil }
func (s *identityLedger) RemoveRoot(string) error                  { return nil }
func (s *identityLedger) AppendBatch([]store.HistoryEvent) error   { return nil }
func (s *identityLedger) GroupAliases() (map[string]string, error) { return nil, nil }
func (s *identityLedger) RecordWorktreeIdentity(rec store.WorktreeIdentity) error {
	if s.fail > 0 {
		s.fail--
		return errors.New("ledger unavailable")
	}
	s.got = append(s.got, rec)
	return nil
}

func TestWorktreeIdentityIsWrittenOnlyWhenNewOrRenamed(t *testing.T) {
	l := New(Options{})
	l.ensureIndex()
	cfg := &groups.Config{Dir: t.TempDir(), Name: "demo", Path: "oberth.yaml"}
	ledger := &identityLedger{}
	if l.recordIdentity(nil, "token", cfg) || l.recordIdentity(ledger, "token", nil) ||
		l.recordIdentity(ledger, "token", &groups.Config{}) || l.recordIdentity(ledger, "", cfg) {
		t.Fatal("a missing store, config, directory, or token was written")
	}
	if len(ledger.got) != 0 {
		t.Fatal("a refused identity reached the ledger")
	}

	l.attr.identities = nil
	ledger.fail = 1
	if l.recordIdentity(ledger, "token", cfg) {
		t.Fatal("a failed write was reported as recorded")
	}
	if _, cached := l.attr.identities["token"]; cached {
		t.Fatal("a failed write was cached, so a retry would skip the ledger")
	}
	if !l.recordIdentity(ledger, "token", cfg) || len(ledger.got) != 1 || ledger.got[0].Identity != "token" {
		t.Fatalf("retry did not record the identity: %+v", ledger.got)
	}
	if !l.recordIdentity(ledger, "token", cfg) || len(ledger.got) != 1 {
		t.Fatal("an unchanged group name was written again")
	}
}

type blockingRoots struct {
	identityLedger
	started chan struct{}
	release chan struct{}
}

func (s *blockingRoots) Roots() ([]string, error) {
	close(s.started)
	<-s.release
	return nil, nil
}

func TestAttributionStopsWhenTheCallerLeavesDuringTheStoreRead(t *testing.T) {
	st := &blockingRoots{started: make(chan struct{}), release: make(chan struct{})}
	l := loopWith(st)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := l.attributeContext(ctx, nil, false)
		done <- err
	}()
	<-st.started
	cancel()
	close(st.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("attribution after cancel = %v, want the caller's cancellation", err)
	}
}

func TestBusyAdmissionChoosesCancelControlOrCache(t *testing.T) {
	l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) {
		return []ports.ListeningPort{{Port: 3000, PID: 7, Process: "app"}}, nil
	}})
	if _, _, changed, err := l.scanLocked(Include{}, Include{}, false); err != nil || !changed {
		t.Fatalf("seed scan changed=%v err=%v", changed, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.scanWhileBusy(ctx, mayCoalesce, ScanLockBudget); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled admission = %v, want the caller's cancellation", err)
	}
	if _, err := l.scanWhileBusy(context.Background(), forceScan, ScanLockBudget); err == nil || !strings.Contains(err.Error(), "fresh observation unavailable") {
		t.Fatalf("forced admission = %v, want the busy refusal", err)
	}
	snap, err := l.scanWhileBusy(context.Background(), mayCoalesce, ScanLockBudget)
	if err != nil || len(snap.Ports) != 1 || snap.Ports[0].Port != 3000 {
		t.Fatalf("read admission = %+v err=%v, want the cached port", snap.Ports, err)
	}
}

func TestReadCancelledWhileTheScannerGateIsHeld(t *testing.T) {
	var scans int
	l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) {
		scans++
		return nil, nil
	}})
	lock(l.rpcGate)
	defer unlock(l.rpcGate)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := l.SnapshotAllContext(ctx, Include{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read while gate held = %v, want the caller's deadline", err)
	}
	if scans != 0 {
		t.Fatalf("cancelled read scanned %d times", scans)
	}
}

func TestBusyReadServesTheCacheAndBusyControlFails(t *testing.T) {
	clock := time.Now()
	var scans int
	l := New(Options{
		Now: func() time.Time { return clock },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			scans++
			return []ports.ListeningPort{{Port: 3000, PID: 7, Process: "app"}}, nil
		},
	})
	if _, _, changed, err := l.scanLocked(Include{}, Include{}, false); err != nil || !changed {
		t.Fatalf("seed scan changed=%v err=%v", changed, err)
	}
	clock = clock.Add(CacheTTL + time.Second)
	lock(l.rpcGate)
	defer unlock(l.rpcGate)

	// The gate budget is supplied instead of waited out: the decision under
	// test is what a caller does when admission is refused, not how long the
	// production budget is. The constants themselves are pinned separately.
	const budget = 50 * time.Millisecond
	var wg sync.WaitGroup
	wg.Add(2)
	var read state.Snapshot
	var readErr error
	var controlErr error
	go func() {
		defer wg.Done()
		read, readErr = l.scanNowWithin(context.Background(), Include{}, mayCoalesce, budget)
	}()
	go func() {
		defer wg.Done()
		_, controlErr = l.scanNowWithin(context.Background(), Include{}, forceScan, budget)
	}()
	wg.Wait()

	// Neither result is a new scan: the read falls back to the cache, the
	// control request refuses.
	if readErr != nil && !errors.Is(readErr, context.DeadlineExceeded) {
		t.Fatalf("busy read = %v, want the cache or the caller's deadline", readErr)
	}
	if readErr == nil && (len(read.Ports) != 1 || read.Ports[0].Port != 3000) {
		t.Fatalf("busy read served %+v, want the cached port", read.Ports)
	}
	if controlErr == nil || (!errors.Is(controlErr, context.DeadlineExceeded) && !strings.Contains(controlErr.Error(), "fresh observation unavailable")) {
		t.Fatalf("forced scan while busy = %v, want a refusal", controlErr)
	}
	if scans != 1 {
		t.Fatalf("busy wait scanned %d times, want the seed scan only", scans)
	}
}

// TestProbeContextPrefersTheContextAwareSeam: ProbeContext carries the scan's
// ownership, so it wins over the legacy synchronous Probe. A caller who has
// already left gets neither a probe nor a verdict — cancellation is not a
// health result.
func TestProbeContextPrefersTheContextAwareSeam(t *testing.T) {
	var legacy, contextual int
	l := New(Options{
		Probe: func(string, int, string, time.Duration) ports.HealthResult {
			legacy++
			return ports.HealthResult{Status: "legacy"}
		},
		ProbeContext: func(context.Context, string, int, string, time.Duration) ports.HealthResult {
			contextual++
			return ports.HealthResult{Status: "contextual"}
		},
	})

	got := l.ProbeContext(context.Background(), "127.0.0.1", 8080, "/healthz", time.Second)
	if got.Status != "contextual" || contextual != 1 || legacy != 0 {
		t.Fatalf("probe = %+v contextual=%d legacy=%d, want the context-aware seam only", got, contextual, legacy)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := l.ProbeContext(cancelled, "127.0.0.1", 8080, "/healthz", time.Second); got.Status != "" || contextual != 1 {
		t.Fatalf("cancelled probe = %+v contextual=%d, want no probe and no verdict", got, contextual)
	}
}
