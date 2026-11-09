package scanner

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// The loop's smoke suite. Each test drives the surface a caller has —
// scanAndPublish, Rescan, Snapshot, Cached, Status — and checks the fact that
// caller would see; none of them reaches into the commit internals.

// TestCadenceConstantsArePinned pins the cadence contract in literals. The
// other cadence tests compare against the constants themselves, so a constant
// that moved would move its own expectation with it; these are the values
// `daemon.status` and the spec promise.
func TestCadenceConstantsArePinned(t *testing.T) {
	cases := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"BaseInterval", BaseInterval, 2 * time.Second},
		{"MinScanInterval", MinScanInterval, time.Second},
		{"MaxInterval", MaxInterval, 10 * time.Second},
		{"SubscribedMaxInterval", SubscribedMaxInterval, 5 * time.Second},
		{"CacheTTL", CacheTTL, 2 * time.Second},
		{"StatsInterval", StatsInterval, time.Second},
		{"MinStatsInterval", MinStatsInterval, 250 * time.Millisecond},
		{"HealthCadence", HealthCadence, 10 * time.Second},
		{"HealthTimeout", HealthTimeout, 2 * time.Second},
		{"HealthBudget", HealthBudget, 4 * time.Second},
		{"ConfiguredHealthBudget", ConfiguredHealthBudget, 2 * time.Second},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %s, want %s", tc.name, tc.got, tc.want)
		}
	}
}

func fixedRows(rows ...ports.ListeningPort) func(Include) ([]ports.ListeningPort, error) {
	return func(Include) ([]ports.ListeningPort, error) {
		return append([]ports.ListeningPort(nil), rows...), nil
	}
}

func oneSubscriber() (int, Include) { return 1, Include{} }

func noSubscribers() (int, Include) { return 0, Include{} }

func msOf(d time.Duration) int { return int(d / time.Millisecond) }

// TestBackoffSequence pins the adaptive interval: base 2 s, ×1.5 per unchanged
// tick, capped at 10 s when only RPC reads are left and 5 s while somebody is
// subscribed.
func TestBackoffSequence(t *testing.T) {
	cases := []struct {
		name string
		max  time.Duration
		want []time.Duration
	}{
		{"rpc reads only", MaxInterval, []time.Duration{3 * time.Second, 4500 * time.Millisecond, 6750 * time.Millisecond, 10 * time.Second, 10 * time.Second}},
		{"a subscriber is connected", SubscribedMaxInterval, []time.Duration{3 * time.Second, 4500 * time.Millisecond, 5 * time.Second, 5 * time.Second}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BaseInterval
			for step, want := range tc.want {
				got = backoff(got, BaseInterval, tc.max)
				if got != want {
					t.Fatalf("step %d = %s, want %s", step+1, got, want)
				}
			}
		})
	}
}

func TestBackoffNeverDropsBelowBase(t *testing.T) {
	if got := backoff(time.Millisecond, BaseInterval, MaxInterval); got != BaseInterval {
		t.Fatalf("backoff(1ms) = %s, want %s", got, BaseInterval)
	}
}

// TestLoopBacksOffThenSnapsBack: unchanged ticks grow the interval; the first
// change puts it back at the base.
func TestLoopBacksOffThenSnapsBack(t *testing.T) {
	var mu sync.Mutex
	rows := []ports.ListeningPort{{Port: 3000, BindAddress: "127.0.0.1", PID: 100, Process: "node"}}
	l := New(Options{
		Demand: oneSubscriber,
		Scan: func(Include) ([]ports.ListeningPort, error) {
			mu.Lock()
			defer mu.Unlock()
			return append([]ports.ListeningPort(nil), rows...), nil
		},
	})

	l.scanAndPublish(Include{})
	if got := l.Status().IntervalMs; got != msOf(BaseInterval) {
		t.Fatalf("first scan interval = %dms, want %d", got, msOf(BaseInterval))
	}
	l.scanAndPublish(Include{})
	l.scanAndPublish(Include{})
	if got := l.Status().IntervalMs; got <= msOf(BaseInterval) {
		t.Fatalf("unchanged scans did not back off: %dms", got)
	}

	mu.Lock()
	rows = append(rows, ports.ListeningPort{Port: 8123, BindAddress: "127.0.0.1", PID: 200, Process: "python3"})
	mu.Unlock()
	l.scanAndPublish(Include{})
	if got := l.Status().IntervalMs; got != msOf(BaseInterval) {
		t.Fatalf("interval after a change = %dms, want %d", got, msOf(BaseInterval))
	}
}

func TestBackoffCapsAtMaxInterval(t *testing.T) {
	l := New(Options{
		Demand: noSubscribers,
		Scan:   fixedRows(ports.ListeningPort{Port: 3000, BindAddress: "127.0.0.1", PID: 1}),
	})
	for range 20 {
		l.scanAndPublish(Include{})
	}
	if got := l.Status().IntervalMs; got != msOf(MaxInterval) {
		t.Fatalf("interval = %dms, want the %dms cap", got, msOf(MaxInterval))
	}
}

func TestBackoffCapsAtFiveSecondsWithASubscriber(t *testing.T) {
	var subscribers atomic.Int64
	l := New(Options{
		Demand: func() (int, Include) { return int(subscribers.Load()), Include{} },
		Scan:   fixedRows(ports.ListeningPort{Port: 3000, BindAddress: "127.0.0.1", PID: 1}),
	})

	subscribers.Store(1)
	for range 20 {
		l.scanAndPublish(Include{})
	}
	if got := l.Status().IntervalMs; got != msOf(SubscribedMaxInterval) {
		t.Fatalf("interval with a subscriber = %dms, want %d", got, msOf(SubscribedMaxInterval))
	}

	// The last subscriber leaves: the loop may back off the rest of the way.
	subscribers.Store(0)
	for range 20 {
		l.scanAndPublish(Include{})
	}
	if got := l.Status().IntervalMs; got != msOf(MaxInterval) {
		t.Fatalf("interval with no subscribers = %dms, want %d", got, msOf(MaxInterval))
	}
}

// TestPublishesAddAndRemove is the delta contract clients depend on: a new
// listener arrives in Ports.Added, a closed one in Ports.Removed.
func TestPublishesAddAndRemove(t *testing.T) {
	var mu sync.Mutex
	var rows []ports.ListeningPort
	var deltas []state.Delta
	l := New(Options{
		Demand: oneSubscriber,
		Scan: func(Include) ([]ports.ListeningPort, error) {
			mu.Lock()
			defer mu.Unlock()
			return append([]ports.ListeningPort(nil), rows...), nil
		},
		Publish: func(prev, next state.Snapshot, _ []state.Event) {
			deltas = append(deltas, state.Diff(prev, next))
		},
	})

	l.scanAndPublish(Include{})
	mu.Lock()
	rows = append(rows, ports.ListeningPort{Port: 8123, BindAddress: "127.0.0.1", PID: 42, Process: "python3"})
	mu.Unlock()
	l.scanAndPublish(Include{})
	if last := deltas[len(deltas)-1]; len(last.Ports.Added) != 1 || last.Ports.Added[0].Port != 8123 {
		t.Fatalf("expected 8123 in Added, got %+v", last.Ports)
	}

	mu.Lock()
	rows = nil
	mu.Unlock()
	l.scanAndPublish(Include{})
	if last := deltas[len(deltas)-1]; len(last.Ports.Removed) != 1 || last.Ports.Removed[0] != "8123:127.0.0.1" {
		t.Fatalf("expected 8123:127.0.0.1 in Removed, got %+v", last.Ports)
	}
}

// TestSeqIsMonotonic: Seq advances on a change and only on a change.
func TestSeqIsMonotonic(t *testing.T) {
	var mu sync.Mutex
	n := 0
	l := New(Options{
		Demand: oneSubscriber,
		Scan: func(Include) ([]ports.ListeningPort, error) {
			mu.Lock()
			defer mu.Unlock()
			out := make([]ports.ListeningPort, n)
			for i := range out {
				out[i] = ports.ListeningPort{Port: 3000 + i, BindAddress: "127.0.0.1", PID: 1000 + i}
			}
			return out, nil
		},
	})

	var seqs []uint64
	for range 4 {
		l.scanAndPublish(Include{})
		seqs = append(seqs, l.Status().Seq)
		mu.Lock()
		n++
		mu.Unlock()
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("seq did not advance: %v", seqs)
		}
	}

	before := l.Status().Seq
	l.scanAndPublish(Include{})
	l.scanAndPublish(Include{})
	if got := l.Status().Seq; got != before+1 {
		t.Fatalf("seq = %d after one change and one unchanged scan, want %d", got, before+1)
	}
}

// TestScanErrorKeepsLastGoodState: a failing collector emits scan_error and
// leaves the last good snapshot alone.
func TestScanErrorKeepsLastGoodState(t *testing.T) {
	var fail atomic.Bool
	var events []state.Event
	l := New(Options{
		Demand: oneSubscriber,
		Scan: func(Include) ([]ports.ListeningPort, error) {
			if fail.Load() {
				return nil, errors.New("lsof: not found")
			}
			return []ports.ListeningPort{{Port: 3000, BindAddress: "127.0.0.1", PID: 7}}, nil
		},
		Publish: func(_, _ state.Snapshot, ev []state.Event) { events = append(events, ev...) },
	})

	l.scanAndPublish(Include{})
	good := l.Cached()
	fail.Store(true)
	l.scanAndPublish(Include{})

	if got := l.Cached(); got.Seq != good.Seq || len(got.Ports) != 1 {
		t.Fatalf("failed scan clobbered the last good snapshot: %+v", got)
	}
	for _, ev := range events {
		if ev.Kind == "scan_error" {
			return
		}
	}
	t.Fatalf("no scan_error event emitted; got %+v", events)
}

// TestSnapshotUsesTheCacheWithinTTL: two reads inside CacheTTL cost one scan.
func TestSnapshotUsesTheCacheWithinTTL(t *testing.T) {
	var scans atomic.Int64
	l := New(Options{
		Demand: noSubscribers,
		Scan: func(Include) ([]ports.ListeningPort, error) {
			scans.Add(1)
			return []ports.ListeningPort{{Port: 3000, BindAddress: "127.0.0.1", PID: 7}}, nil
		},
	})

	for range 2 {
		if _, err := l.Snapshot(Include{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := scans.Load(); got != 1 {
		t.Fatalf("two reads inside %s ran %d scans, want 1", CacheTTL, got)
	}
}

// TestLoopParksWithNoSubscribers: no subscribers, no scans, until something
// wakes the loop.
func TestLoopParksWithNoSubscribers(t *testing.T) {
	var scans atomic.Int64
	var subscribers atomic.Int64
	l := New(Options{
		Demand: func() (int, Include) { return int(subscribers.Load()), Include{} },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			scans.Add(1)
			return nil, nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)

	time.Sleep(150 * time.Millisecond)
	if got := scans.Load(); got != 0 {
		t.Fatalf("loop scanned %d times with no subscribers", got)
	}

	subscribers.Store(1)
	l.Wake()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && scans.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if scans.Load() == 0 {
		t.Fatal("loop did not scan after a subscriber arrived")
	}
}

// TestConfiguredScanIntervalMovesTheWholeCurve: the configured base moves the
// floor and both ceilings, not just the floor.
func TestConfiguredScanIntervalMovesTheWholeCurve(t *testing.T) {
	var subscribers atomic.Int64
	subscribers.Store(1)
	l := New(Options{
		ScanInterval: 5 * time.Second,
		Demand:       func() (int, Include) { return int(subscribers.Load()), Include{} },
		Scan:         fixedRows(ports.ListeningPort{Port: 3000, BindAddress: "127.0.0.1", PID: 1}),
	})

	l.scanAndPublish(Include{})
	if got := l.Status().IntervalMs; got != 5000 {
		t.Fatalf("interval = %dms, want the configured base 5000", got)
	}
	for range 20 {
		l.scanAndPublish(Include{})
	}
	if got := l.Status().IntervalMs; got != 12500 {
		t.Fatalf("subscribed cap = %dms, want 12500", got)
	}

	subscribers.Store(0)
	for range 20 {
		l.scanAndPublish(Include{})
	}
	if got := l.Status().IntervalMs; got != 25000 {
		t.Fatalf("idle cap = %dms, want 25000", got)
	}
}

// TestConfiguredScanIntervalPacesTheTick: the wait between ticks is the
// configured base, measured against an injected clock rather than a sleep.
func TestConfiguredScanIntervalPacesTheTick(t *testing.T) {
	frozen := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	now := frozen
	l := New(Options{
		ScanInterval: 6 * time.Second,
		Now:          func() time.Time { return now },
		Demand:       oneSubscriber,
		Scan:         fixedRows(ports.ListeningPort{Port: 3000, BindAddress: "127.0.0.1", PID: 1}),
	})

	l.scanAndPublish(Include{})
	if got := l.dueIn(); got != 6*time.Second {
		t.Fatalf("dueIn right after a scan = %s, want 6s", got)
	}
	now = frozen.Add(5 * time.Second)
	if got := l.dueIn(); got != time.Second {
		t.Fatalf("dueIn 5s into a 6s base = %s, want 1s", got)
	}
	now = frozen.Add(6 * time.Second)
	if got := l.dueIn(); got > 0 {
		t.Fatalf("dueIn once the base elapsed = %s, want a scan due", got)
	}
}

// TestScanIntervalIsClampedToTheFloor: a ten-millisecond request is clamped,
// not honoured.
func TestScanIntervalIsClampedToTheFloor(t *testing.T) {
	l := New(Options{ScanInterval: 10 * time.Millisecond})
	if got := l.Status().BaseIntervalMs; got != msOf(MinScanInterval) {
		t.Fatalf("base = %dms, want the %dms floor", got, msOf(MinScanInterval))
	}
}

// TestStatusReportsTheEffectiveIntervals: both cadences are fixed at startup,
// so `daemon.status` is the only way to tell what a running daemon settled on.
func TestStatusReportsTheEffectiveIntervals(t *testing.T) {
	st := New(Options{ScanInterval: 3 * time.Second, StatsInterval: 500 * time.Millisecond}).Status()
	if st.BaseIntervalMs != 3000 || st.StatsIntervalMs != 500 {
		t.Fatalf("status = %+v, want base 3000 and stats 500", st)
	}
	def := New(Options{}).Status()
	if def.BaseIntervalMs != msOf(BaseInterval) || def.StatsIntervalMs != msOf(StatsInterval) {
		t.Fatalf("default status = %+v, want %s and %s", def, BaseInterval, StatsInterval)
	}
}

// TestASlowScanNeverCommitsOverANewerOne is the "no going backwards" rule: a
// scan that began before a newer one cannot put its older observation on the
// wire, even when it finishes later.
func TestASlowScanNeverCommitsOverANewerOne(t *testing.T) {
	var calls atomic.Int32
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})

	var mu sync.Mutex
	var published []state.Snapshot
	l := New(Options{
		Demand: oneSubscriber,
		Scan: func(Include) ([]ports.ListeningPort, error) {
			if calls.Add(1) == 1 {
				close(firstStarted)
				<-releaseFirst
				return []ports.ListeningPort{{Port: 4000, BindAddress: "127.0.0.1", PID: 40, Process: "old"}}, nil
			}
			return []ports.ListeningPort{{Port: 5000, BindAddress: "127.0.0.1", PID: 50, Process: "new"}}, nil
		},
		Publish: func(_, next state.Snapshot, _ []state.Event) {
			mu.Lock()
			published = append(published, next)
			mu.Unlock()
		},
	})

	staleDone := make(chan struct{})
	go func() {
		defer close(staleDone)
		_, _ = l.Rescan(Include{})
	}()
	<-firstStarted

	// The tick runs while the forced scan is still out: the two gates are
	// separate, so its newer observation is what goes out.
	l.scanAndPublish(Include{})
	close(releaseFirst)
	<-staleDone

	final := l.Cached()
	if len(final.Ports) != 1 || final.Ports[0].Port != 5000 {
		t.Fatalf("final snapshot = %+v, want port 5000", final.Ports)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, snap := range published {
		for _, p := range snap.Ports {
			if p.Port == 4000 {
				t.Fatalf("the stale scan reached the wire: %+v", snap.Ports)
			}
		}
	}
	for i := 1; i < len(published); i++ {
		if published[i].Seq < published[i-1].Seq {
			t.Fatalf("seq went backwards: %d -> %d", published[i-1].Seq, published[i].Seq)
		}
	}
}
