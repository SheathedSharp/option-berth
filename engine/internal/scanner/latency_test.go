package scanner

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// What a handler pays for, not what it publishes: each test below checks the
// latency rule the daemon promises (republish re-attributes instead of
// scanning, forced scans never coalesce, an older observation never wins).

// project is a throwaway checkout with one manifest, and a store that knows it.
func project(t *testing.T, body string) (Store, string) {
	t.Helper()
	dir := t.TempDir()
	writeConfig(t, dir, body)
	return openStore(t), dir
}

func onePort(dir string) func(Include) ([]ports.ListeningPort, error) {
	return func(Include) ([]ports.ListeningPort, error) {
		return []ports.ListeningPort{{Port: 8123, PID: 42, Process: "python3", Cwd: dir}}, nil
	}
}

// TestRepublishDoesNotScan: a store write's republish re-attributes the rows
// the last scan already produced; it never asks the OS again.
func TestRepublishDoesNotScan(t *testing.T) {
	st, dir := project(t, "name: before\nports: [8123]\n")
	var scans atomic.Int64
	l := New(Options{
		DaemonVersion: "test",
		Store:         st,
		Scan: func(include Include) ([]ports.ListeningPort, error) {
			scans.Add(1)
			return onePort(dir)(include)
		},
	})

	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := scans.Load(); got != 1 {
		t.Fatalf("scans after the first read = %d, want 1", got)
	}
	writeConfig(t, dir, "name: shop\nports: [8123]\n")
	if err := l.Republish(); err != nil {
		t.Fatalf("Republish: %v", err)
	}
	if got := scans.Load(); got != 1 {
		t.Fatalf("scans after Republish = %d, want the first read only", got)
	}
	cached := l.Cached()
	if len(cached.Ports) != 1 || deref(cached.Ports[0].Group) != "shop" {
		t.Fatalf("cached = %+v, want the renamed group published without a scan", cached.Ports)
	}
}

// TestRepublishPublishesBeforeItReturns: the delta carrying the change is on
// the wire before the handler replies.
func TestRepublishPublishesBeforeItReturns(t *testing.T) {
	st, dir := project(t, "name: before\nports: [8123]\n")
	var mu sync.Mutex
	var groups []string
	l := New(Options{
		DaemonVersion: "test",
		Store:         st,
		Demand:        oneSubscriber,
		Scan:          onePort(dir),
		Publish: func(_, next state.Snapshot, _ []state.Event) {
			mu.Lock()
			defer mu.Unlock()
			for _, p := range next.Ports {
				groups = append(groups, deref(p.Group))
			}
		},
	})
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	writeConfig(t, dir, "name: shop\nports: [8123]\n")
	if err := l.Republish(); err != nil {
		t.Fatalf("Republish: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(groups) == 0 || groups[len(groups)-1] != "shop" {
		t.Fatalf("published groups = %v, want shop by the time Republish returned", groups)
	}
}

// TestRepublishBeforeAnyScanFallsBackToOne: nothing to re-attribute yet, so the
// write still has to reach the wire through one real scan.
func TestRepublishBeforeAnyScanFallsBackToOne(t *testing.T) {
	var scans atomic.Int64
	l := New(Options{
		DaemonVersion: "test",
		Scan: func(Include) ([]ports.ListeningPort, error) {
			scans.Add(1)
			return []ports.ListeningPort{{Port: 8123, PID: 42, Process: "python3"}}, nil
		},
	})
	if err := l.Republish(); err != nil {
		t.Fatalf("Republish: %v", err)
	}
	if got := scans.Load(); got != 1 {
		t.Fatalf("scans = %d, want one fallback scan", got)
	}
}

// TestRPCScanDoesNotInheritDemandButKeepsIt: an RPC scan collects only what its
// caller asked for, and carries the subscribers' last readings forward.
func TestRPCScanDoesNotInheritDemandButKeepsIt(t *testing.T) {
	var mu sync.Mutex
	var asked []Include
	l := New(Options{
		DaemonVersion: "test",
		Demand:        func() (int, Include) { return 1, Include{Stats: true, Health: true} },
		Scan: func(inc Include) ([]ports.ListeningPort, error) {
			mu.Lock()
			asked = append(asked, inc)
			mu.Unlock()
			row := ports.ListeningPort{Port: 8123, PID: 42, Process: "python3"}
			if inc.Stats {
				row.CPUPercent, row.MemoryRSS = 12.5, 4096
			}
			if inc.Health {
				row.HealthStatus = "healthy"
			}
			return []ports.ListeningPort{row}, nil
		},
	})

	l.scanAndPublish(Include{Stats: true, Health: true})
	ticked := l.Cached()
	if len(ticked.Ports) != 1 || ticked.Ports[0].Health == nil {
		t.Fatalf("the tick published %+v, want a probed row", ticked.Ports)
	}
	verdict := *ticked.Ports[0].Health

	snap, err := l.Rescan(Include{})
	if err != nil {
		t.Fatalf("Rescan: %v", err)
	}
	mu.Lock()
	got := append([]Include(nil), asked...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("scans = %d, want the tick and the rescan", len(got))
	}
	if got[1].Stats || got[1].Health {
		t.Fatalf("the rescan collected %+v, want a bare scan", got[1])
	}
	if len(snap.Ports) != 1 {
		t.Fatalf("ports = %+v", snap.Ports)
	}
	if snap.Ports[0].Stats == nil || snap.Ports[0].Stats.CPUPercent != 12.5 {
		t.Fatalf("stats = %+v, want the subscriber's reading carried forward", snap.Ports[0].Stats)
	}
	if snap.Ports[0].Health == nil || *snap.Ports[0].Health != verdict {
		t.Fatalf("health = %+v, want %+v carried forward", snap.Ports[0].Health, verdict)
	}
}

// TestOlderScanNeverOvertakesANewerOne: overlapping scans are fine, going
// backwards is not. The scan that began first cannot commit after a newer one.
func TestOlderScanNeverOvertakesANewerOne(t *testing.T) {
	release := make(chan struct{})
	var slow atomic.Bool
	l := New(Options{
		DaemonVersion: "test",
		Scan: func(inc Include) ([]ports.ListeningPort, error) {
			if inc.Stats {
				slow.Store(true)
				<-release
				return []ports.ListeningPort{
					{Port: 8123, PID: 42, Process: "python3"},
					{Port: 8124, PID: 43, Process: "python3"},
				}, nil
			}
			return []ports.ListeningPort{{Port: 8123, PID: 42, Process: "python3"}}, nil
		},
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		l.scanAndPublish(Include{Stats: true})
	}()
	for !slow.Load() {
		time.Sleep(time.Millisecond)
	}
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatalf("Rescan: %v", err)
	}
	// The newer scan must have committed before the older one finishes, or the
	// assertion at the end would pass without exercising the ordering.
	if got := len(l.Cached().Ports); got != 1 {
		t.Fatalf("ports after the newer scan = %d, want 1", got)
	}

	close(release)
	<-done
	if got := len(l.Cached().Ports); got != 1 {
		t.Fatalf("ports after the older scan finished = %d, want the newer snapshot", got)
	}
}

// TestLockForGivesUp: a held gate expires on its budget instead of turning a
// handler's wait into a reply that never comes.
func TestLockForGivesUp(t *testing.T) {
	gate := make(chan struct{}, 1)
	lock(gate)
	start := time.Now()
	if lockFor(gate, 20*time.Millisecond) {
		t.Fatal("lockFor took a held gate")
	}
	if waited := time.Since(start); waited < 20*time.Millisecond {
		t.Fatalf("gave up after %s, want at least the budget", waited)
	}
	unlock(gate)
	if !lockFor(gate, time.Second) {
		t.Fatal("lockFor could not take a free gate")
	}
	unlock(gate)
}

// TestConfiguredHealthRoundIsBudgeted: forty slow probes cannot add waves of
// HealthTimeout to the scan every write queues behind.
func TestConfiguredHealthRoundIsBudgeted(t *testing.T) {
	rows := make([]state.Port, 0, 40)
	path := "/healthz"
	gg := []state.Group{{Name: "slow"}}
	for i := range 40 {
		port := 9000 + i
		rows = append(rows, state.Port{Port: port, BindAddress: "127.0.0.1"})
		gg[0].Services = append(gg[0].Services, state.Service{Name: "svc", Health: &path, PortActual: &port})
	}

	start := time.Now()
	probeConfigured(rows, gg, func(string, int, string, time.Duration) ports.HealthResult {
		time.Sleep(50 * time.Millisecond)
		return ports.HealthResult{Status: "timeout"}
	}, 80*time.Millisecond)
	if took := time.Since(start); took > 400*time.Millisecond {
		t.Fatalf("a round of 40 probes took %s, want it bounded by the budget", took)
	}
	if rows[0].Health == nil {
		t.Fatal("the first probe should still have run and landed")
	}
}

// TestRescanAlwaysScansOnAStoppedClock: the Windows regression. Coalescing used
// timestamps, and time.Now on Windows steps in ~15 ms, so a kill's rescan was
// answered with the snapshot it was not allowed to use. The clock is frozen
// here on purpose; a forced scan must still scan.
func TestRescanAlwaysScansOnAStoppedClock(t *testing.T) {
	frozen := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	var scans atomic.Int64
	l := New(Options{
		DaemonVersion: "test",
		Now:           func() time.Time { return frozen },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			scans.Add(1)
			return []ports.ListeningPort{{Port: 8123, PID: 42, Process: "python3"}}, nil
		},
	})

	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for want := int64(2); want <= 4; want++ {
		if _, err := l.Rescan(Include{}); err != nil {
			t.Fatalf("Rescan: %v", err)
		}
		if got := scans.Load(); got != want {
			t.Fatalf("scans = %d, want %d: a forced scan never coalesces", got, want)
		}
	}
}

// TestReadDoesNotCoalesceOntoAScanThatBeganBeforeIt: a read arriving mid-scan
// scans for itself, because that scan saw a machine the caller has not.
func TestReadDoesNotCoalesceOntoAScanThatBeganBeforeIt(t *testing.T) {
	frozen := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	entered := make(chan struct{})
	release := make(chan struct{})
	var scans atomic.Int64
	l := New(Options{
		DaemonVersion: "test",
		Now:           func() time.Time { return frozen },
		Scan: func(inc Include) ([]ports.ListeningPort, error) {
			scans.Add(1)
			if inc.Stats {
				close(entered)
				<-release
			}
			return []ports.ListeningPort{{Port: 8123, PID: 42, Process: "python3"}}, nil
		},
	})

	tick := make(chan struct{})
	go func() {
		defer close(tick)
		l.scanAndPublish(Include{Stats: true})
	}()
	<-entered

	// The tick holds the run gate, not the RPC gate: the read runs and returns
	// while the tick is still blocked, so nothing here races its commit.
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := scans.Load(); got != 2 {
		t.Fatalf("scans = %d, want the read to scan for itself", got)
	}
	close(release)
	<-tick
}

// TestCoalescingComparesScanNumbersNotTheClock states the rule without
// goroutines: a read may take a scan that began after it asked, never the one
// already running, and never a health or uncovered-stats read.
func TestCoalescingComparesScanNumbersNotTheClock(t *testing.T) {
	frozen := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	l := New(Options{
		DaemonVersion: "test",
		Now:           func() time.Time { return frozen },
		Scan:          fixedRows(ports.ListeningPort{Port: 8123, PID: 42, Process: "python3"}),
	})
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := l.scanGeneration(); got != 1 {
		t.Fatalf("scan generation = %d, want 1", got)
	}
	if _, ok := l.scannedSince(0, Include{}); !ok {
		t.Error("a scan that began after the caller asked should answer it")
	}
	if _, ok := l.scannedSince(1, Include{}); ok {
		t.Error("the scan already running must not answer a later caller")
	}
	if _, ok := l.scannedSince(0, Include{Health: true}); ok {
		t.Error("a health read must never coalesce")
	}
	if _, ok := l.scannedSince(0, Include{Stats: true}); ok {
		t.Error("a stats read must not coalesce onto a snapshot without stats")
	}
}

// TestAWriteDuringASlowScanStillPublishesItsChange: attribution runs under the
// ordering gate, so a write that lands during the OS half is part of the very
// snapshot that scan commits.
func TestAWriteDuringASlowScanStillPublishesItsChange(t *testing.T) {
	st, dir := project(t, "name: before\nports: [8123]\n")
	scanStarted := make(chan struct{})
	releaseScan := make(chan struct{})
	var calls atomic.Int32
	l := New(Options{
		DaemonVersion: "test",
		Store:         st,
		Demand:        oneSubscriber,
		Scan: func(Include) ([]ports.ListeningPort, error) {
			if calls.Add(1) == 2 {
				close(scanStarted)
				<-releaseScan
			}
			return onePort(dir)(Include{})
		},
	})

	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	slowDone := make(chan struct{})
	go func() {
		defer close(slowDone)
		_, _ = l.Rescan(Include{})
	}()
	<-scanStarted

	writeConfig(t, dir, "name: shop\nports: [8123]\n")
	if err := l.Republish(); err != nil {
		t.Fatalf("Republish: %v", err)
	}
	close(releaseScan)
	<-slowDone

	final := l.Cached()
	if len(final.Ports) != 1 || deref(final.Ports[0].Group) != "shop" {
		t.Fatalf("final = %+v, want the write's group: the slow scan overwrote it", final.Ports)
	}
}
