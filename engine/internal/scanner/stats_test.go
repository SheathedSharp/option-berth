package scanner

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// The stats tick's smoke rig: a loop whose port scan is a fixed row set and
// whose sampler counts its own calls, so a test can tell a sample from a scan.

type published struct {
	prev, next state.Snapshot
}

type statsRig struct {
	loop *Loop

	mu     sync.Mutex
	rows   []ports.ListeningPort
	gone   map[int]bool
	deltas []published

	scans   atomic.Int64
	samples atomic.Int64
	subs    atomic.Int64
	include atomic.Bool
}

func newStatsRig(t *testing.T) *statsRig {
	t.Helper()
	r := &statsRig{
		gone: map[int]bool{},
	}
	r.rows = []ports.ListeningPort{
		{Port: 3311, BindAddress: "127.0.0.1", PID: 101, Process: "deno", CPUPercent: 1, MemoryRSS: 1 << 20, Connections: 7, ThreadCount: 4},
		{Port: 5277, BindAddress: "127.0.0.1", PID: 202, Process: "esbuild", CPUPercent: 2, MemoryRSS: 2 << 20, Connections: 3, ThreadCount: 9},
	}
	r.loop = New(Options{
		DaemonVersion: "test",
		StatsInterval: 30 * time.Millisecond,
		Demand: func() (int, Include) {
			wanted := Include{Stats: r.include.Load()}
			return int(r.subs.Load()), wanted
		},
		Publish: func(prev, next state.Snapshot, _ []state.Event) {
			r.mu.Lock()
			r.deltas = append(r.deltas, published{prev: prev, next: next})
			r.mu.Unlock()
		},
		Scan: func(Include) ([]ports.ListeningPort, error) {
			r.scans.Add(1)
			r.mu.Lock()
			snapshotRows := append([]ports.ListeningPort(nil), r.rows...)
			r.mu.Unlock()
			return snapshotRows, nil
		},
		SampleStats: func(pids []int) map[int]ports.ProcSample {
			n := r.samples.Add(1)
			r.mu.Lock()
			defer r.mu.Unlock()
			out := make(map[int]ports.ProcSample, len(pids))
			for _, pid := range pids {
				if r.gone[pid] {
					continue
				}
				out[pid] = ports.ProcSample{
					CPUPercent: float64(n), MemoryRSS: int64(pid) << 10,
					ThreadCount: 11, State: "running", Uptime: "1s",
				}
			}
			return out
		},
	})
	return r
}

// run drives the loop in the background until the test finishes.
func (r *statsRig) run(t *testing.T) {
	t.Helper()
	runCtx, stop := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		r.loop.Run(runCtx)
	}()
	t.Cleanup(func() {
		stop()
		<-stopped
	})
}

// subscribe fakes a client connecting with the given include set.
func (r *statsRig) subscribe(withStats bool) {
	r.include.Store(withStats)
	r.subs.Store(1)
	r.loop.Wake()
}

// unsubscribe parks the tick again by dropping the last subscriber.
func (r *statsRig) unsubscribe() {
	r.subs.Store(0)
	r.include.Store(false)
	r.loop.Wake()
}

// published snapshots the deltas the loop has handed over so far.
func (r *statsRig) published() []published {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]published(nil), r.deltas...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	limit := time.Now().Add(5 * time.Second)
	for time.Now().Before(limit) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestStatsTickParksUntilSomeoneSubscribes: an RPC read wakes the loop, but a
// daemon nobody watches must not start a per-second process sampler.
func TestStatsTickParksUntilSomeoneSubscribes(t *testing.T) {
	r := newStatsRig(t)
	r.run(t)

	if _, err := r.loop.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := r.samples.Load(); got != 0 {
		t.Fatalf("sampler ran %d times with no subscribers", got)
	}

	r.subscribe(true)
	waitFor(t, "the sampler to start", func() bool { return r.samples.Load() > 0 })

	r.unsubscribe()
	time.Sleep(100 * time.Millisecond)
	before := r.samples.Load()
	time.Sleep(250 * time.Millisecond)
	if after := r.samples.Load(); after != before {
		t.Errorf("sampler ran %d more times after the last unsubscribe", after-before)
	}
}

// TestStatsTickRunsOnItsOwnCadence: several samples land inside the scan
// cadence, and the scan is not what produced them.
func TestStatsTickRunsOnItsOwnCadence(t *testing.T) {
	r := newStatsRig(t)
	r.run(t)
	r.subscribe(true)

	waitFor(t, "four samples", func() bool { return r.samples.Load() >= 4 })
	if scans := r.scans.Load(); scans > 3 {
		t.Errorf("%d port scans while four samples were taken", scans)
	}
}

// TestStatsTickNeverResetsTheScanInterval: a stats tick is not a scan, so it
// neither moves the backoff nor the scan counter.
func TestStatsTickNeverResetsTheScanInterval(t *testing.T) {
	r := newStatsRig(t)
	for range 4 {
		r.loop.scanAndPublish(Include{Stats: true})
	}
	backedOff := r.loop.Status().IntervalMs
	if backedOff <= msOf(BaseInterval) {
		t.Fatalf("interval = %dms, want a backed-off value", backedOff)
	}
	scans := r.scans.Load()

	for range 5 {
		r.loop.sampleStats(Include{Stats: true})
	}
	if got := r.loop.Status().IntervalMs; got != backedOff {
		t.Errorf("stats tick moved the scan interval: %dms -> %dms", backedOff, got)
	}
	if got := r.loop.Status().Scans; got != scans {
		t.Errorf("scan count = %d, want %d", got, scans)
	}
	if got := r.scans.Load(); got != scans {
		t.Errorf("stats tick called the OS scan %d times", got-scans)
	}
}

// TestStatsTickSuppressesAnEmptyDelta: nothing moved, so nothing is published
// and no sequence number is burned.
func TestStatsTickSuppressesAnEmptyDelta(t *testing.T) {
	r := newStatsRig(t)
	fixed := map[int]ports.ProcSample{
		101: {CPUPercent: 1, MemoryRSS: 1 << 20, ThreadCount: 4, State: "running", Uptime: "1s"},
		202: {CPUPercent: 2, MemoryRSS: 2 << 20, ThreadCount: 9, State: "running", Uptime: "1s"},
	}
	r.loop.opts.SampleStats = func([]int) map[int]ports.ProcSample { return fixed }

	r.loop.scanAndPublish(Include{Stats: true})
	// Settle: the scan's own numbers and the sampler's disagree once.
	r.loop.sampleStats(Include{Stats: true})
	seq := r.loop.Cached().Seq
	published := len(r.published())

	for range 3 {
		r.loop.sampleStats(Include{Stats: true})
	}
	if got := len(r.published()); got != published {
		t.Errorf("%d deltas published where nothing moved", got-published)
	}
	if got := r.loop.Cached().Seq; got != seq {
		t.Errorf("seq = %d, want %d: an unpublished tick burned a number", got, seq)
	}
}

// TestStatsTickHandlesAPIDDisappearing: a vanished pid keeps its last stats;
// zeroes would read like facts, and removing the row is the scan's job.
func TestStatsTickHandlesAPIDDisappearing(t *testing.T) {
	rig := newStatsRig(t)
	rig.loop.scanAndPublish(Include{Stats: true})
	rig.loop.sampleStats(Include{Stats: true})
	before := rig.loop.Cached()

	rig.mu.Lock()
	rig.gone[101] = true
	rig.mu.Unlock()
	rig.loop.sampleStats(Include{Stats: true})
	after := rig.loop.Cached()

	if len(after.Ports) != len(before.Ports) {
		t.Fatalf("the stats tick removed a row: %d -> %d", len(before.Ports), len(after.Ports))
	}
	vanished, alive := after.Ports[0], after.Ports[1]
	if vanished.PID != 101 || alive.PID != 202 {
		t.Fatalf("unexpected row order: %+v", after.Ports)
	}
	if *vanished.Stats != *before.Ports[0].Stats {
		t.Errorf("a vanished pid's stats were rewritten")
	}
	if *alive.Stats == *before.Ports[1].Stats {
		t.Error("the surviving pid's stats were not refreshed")
	}
}

// TestStatsTickIsSerializedWithScans: writers hammering Rescan while the tick
// runs still publish in sequence order.
func TestStatsTickIsSerializedWithScans(t *testing.T) {
	r := newStatsRig(t)
	r.run(t)
	r.subscribe(true)

	var workers sync.WaitGroup
	quit := make(chan struct{})
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-quit:
					return
				default:
				}
				if _, err := r.loop.Rescan(Include{Stats: true}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	// Samples alone do not prove a publish: a scan can supersede a tick before
	// it commits, so wait for both.
	waitFor(t, "five samples and two publishes under load", func() bool {
		return r.samples.Load() >= 5 && len(r.published()) >= 2
	})
	close(quit)
	workers.Wait()

	seen := r.published()
	if len(seen) < 2 {
		t.Fatalf("only %d publishes; nothing interleaved", len(seen))
	}
	var last uint64
	for i, p := range seen {
		if p.next.Seq <= last {
			t.Fatalf("publish %d has seq %d after %d", i, p.next.Seq, last)
		}
		last = p.next.Seq
	}
}

// TestStatsTickRunsWhileAScanIsInFlight: the sampler never waits on a scan
// that is still doing OS work, and its result still lands.
func TestStatsTickRunsWhileAScanIsInFlight(t *testing.T) {
	r := newStatsRig(t)
	r.loop.scanAndPublish(Include{Stats: true})

	entered := make(chan struct{})
	proceed := make(chan struct{})
	var first sync.Once
	fixed := r.rows
	r.loop.opts.Scan = func(Include) ([]ports.ListeningPort, error) {
		first.Do(func() {
			close(entered)
			<-proceed
		})
		return append([]ports.ListeningPort(nil), fixed...), nil
	}

	scanOver := make(chan struct{})
	go func() {
		defer close(scanOver)
		r.loop.scanAndPublish(Include{Stats: true})
	}()
	<-entered

	before := r.loop.Cached().Seq
	tickDone := make(chan struct{})
	go func() {
		defer close(tickDone)
		r.loop.sampleStats(Include{Stats: true})
	}()
	select {
	case <-tickDone:
	case <-time.After(2 * time.Second):
		close(proceed)
		<-scanOver
		t.Fatal("the stats tick blocked behind a scan still doing OS work")
	}
	if got := r.loop.Cached().Seq; got <= before {
		t.Errorf("seq = %d, want more than %d", got, before)
	}

	close(proceed)
	<-scanOver
}

// TestStatsSkipRowsThatAreNotThisMachines: a container's numbers come from
// Docker and a remote row's pid belongs to another host, so the local process
// sampler must not be asked about either.
func TestStatsSkipRowsThatAreNotThisMachines(t *testing.T) {
	pp := []state.Port{
		{Port: 1, PID: 10, Host: state.LocalhostName, Stats: &state.Stats{}},
		{Port: 2, PID: 20, Host: "build-host", Stats: &state.Stats{}},
		{Port: 3, PID: 30, Host: state.LocalhostName, Type: state.TypeDocker, Stats: &state.Stats{}},
		{Port: 4, PID: 0, Host: state.LocalhostName, Stats: &state.Stats{}},
	}
	if got := statsPIDs(pp); len(got) != 1 || got[0] != 10 {
		t.Fatalf("pids = %v, want only the local non-container pid", got)
	}
}

// TestServiceWithoutALiveProcessKeepsNoSample: a stopped service has no live
// process, and a running one without a pid names none either. Neither gets a
// sample: unknown stays nil rather than becoming zero.
func TestServiceWithoutALiveProcessKeepsNoSample(t *testing.T) {
	pid := 42
	samples := map[int]ports.ProcSample{42: {CPUPercent: 5}}
	cases := []struct {
		name string
		svc  state.Service
	}{
		{"stopped", state.Service{Name: "worker", Running: false, PID: &pid}},
		{"running without a pid", state.Service{Name: "worker", Running: true}},
	}
	for _, tc := range cases {
		svc := tc.svc
		if got := sampledServiceStats(&svc, nil, samples); got != nil {
			t.Fatalf("%s: stats = %+v, want nil", tc.name, got)
		}
	}
}

// TestAMissingSampleLeavesTheServiceUntouched: when the sampler answers for
// other pids only, the service keeps its previous (nil) sample. A vanished
// process is dropped from the sample, not zeroed on the wire.
func TestAMissingSampleLeavesTheServiceUntouched(t *testing.T) {
	pid := 42
	groups := []state.Group{{Services: []state.Service{{Name: "worker", Running: true, PID: &pid}}}}
	writeServiceSample(&groups[0].Services[0], map[int]ports.ProcSample{99: {CPUPercent: 5}})
	if groups[0].Services[0].Stats != nil {
		t.Fatalf("stats = %+v, want nil when the sample has nothing for the pid", groups[0].Services[0].Stats)
	}
}

// TestAStaleStatsSampleIsNotPublished: a sample that was taken before a newer
// full-stats scan committed belongs to the older generation. It must not be
// merged into the newer rows and must not reach the publisher; a live probe of
// the branch is what keeps the generation guard honest.
func TestAStaleStatsSampleIsNotPublished(t *testing.T) {
	var publishes atomic.Int32
	var advance sync.Once
	var loop *Loop
	loop = New(Options{
		DaemonVersion: "test",
		Scan: func(Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{{Port: 4000, PID: 42, Process: "worker", CPUPercent: 1, MemoryRSS: 100}}, nil
		},
		Publish: func(_, _ state.Snapshot, _ []state.Event) { publishes.Add(1) },
		SampleStats: func([]int) map[int]ports.ProcSample {
			// While this sample is being taken, a newer full-stats scan commits.
			advance.Do(func() { loop.scanAndPublish(Include{Stats: true}) })
			return map[int]ports.ProcSample{42: {CPUPercent: 99, MemoryRSS: 999, Uptime: "1s"}}
		},
	})

	// A full-stats scan seeds the snapshot and the stats generation.
	loop.scanAndPublish(Include{Stats: true})
	publishes.Store(0)

	loop.sampleStats(Include{Stats: true})

	if got := publishes.Load(); got != 0 {
		t.Fatalf("%d publishes, want none: a stale sample reached the wire", got)
	}
	snap := loop.Cached()
	if len(snap.Ports) != 1 || snap.Ports[0].Stats == nil {
		t.Fatalf("snapshot = %+v, want the scan's row with its stats", snap.Ports)
	}
	if got := snap.Ports[0].Stats.CPUPercent; got != 1 {
		t.Fatalf("cpu = %v, want the scan's 1: a stale sample was merged", got)
	}
}
