package scanner

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// Wake never blocks: both sends use a default. The one-slot buffer is what
// keeps a nudge that arrives while the loop is inside a scan, where nothing
// is receiving. A dropped nudge leaves the loop on the cadence timer instead
// of letting that tick end.
func TestWakeDuringAScanEndsTheTickWithoutWaitingOutTheCadence(t *testing.T) {
	frozen := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	now := frozen
	entered := make(chan struct{})
	release := make(chan struct{})
	var scans atomic.Int32
	rows := []ports.ListeningPort{{Port: 3000, PID: 7, Process: "app"}}
	l := New(Options{
		ScanInterval: MinScanInterval,
		Now:          func() time.Time { return now },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			if scans.Add(1) == 1 {
				return append([]ports.ListeningPort(nil), rows...), nil
			}
			close(entered)
			<-release
			return append([]ports.ListeningPort(nil), rows...), nil
		},
	})
	if _, _, _, err := l.scanLocked(Include{}, Include{}, false); err != nil {
		t.Fatal(err)
	}
	// dueIn is the whole base, so a lost wake sits on a real timer of at
	// least a second. The kept wake ends the tick as soon as the scan returns.
	now = frozen.Add(MinScanInterval)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	timer := time.NewTimer(time.Hour)
	t.Cleanup(func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	})
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		l.waitTick(ctx, timer, Include{})
	}()
	<-entered
	l.Wake()
	close(release)
	select {
	case <-ended:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("a wake that arrived during the scan was dropped; the tick stayed on the cadence timer")
	}
}

// A stats sample is the stretch where statsWake has no receiver. The buffer
// keeps that nudge, so when the tick next parks it resumes instead of waiting
// for a later subscriber.
func TestWakeDuringAStatsSampleResumesTheParkedTick(t *testing.T) {
	var subs atomic.Int32
	subs.Store(1)
	entered := make(chan struct{})
	release := make(chan struct{})
	resumed := make(chan struct{})
	var zeroReads atomic.Int32
	l := New(Options{
		StatsInterval: MinStatsInterval,
		Demand: func() (int, Include) {
			if subs.Load() == 0 {
				if zeroReads.Add(1) == 2 {
					close(resumed)
				}
			}
			return int(subs.Load()), Include{Stats: true}
		},
		SampleStats: func([]int) map[int]ports.ProcSample {
			close(entered)
			<-release
			return map[int]ports.ProcSample{7: {MemoryRSS: 10, State: "running"}}
		},
		Scan: fixedRows(ports.ListeningPort{
			Port: 3000, PID: 7, Process: "app", CPUPercent: 1, MemoryRSS: 10,
		}),
	})
	if _, _, _, err := l.scanLocked(Include{Stats: true}, Include{}, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() {
		defer close(done)
		l.runStats(ctx)
	}()
	<-entered
	subs.Store(0)
	l.Wake()
	close(release)
	select {
	case <-resumed:
	case <-time.After(2 * time.Second):
		t.Fatal("a wake during the sample was dropped; the stats tick parked until some later nudge")
	}
	cancel()
	<-done
}

func TestNilDemandAndPublisherDoNotClearTheInstalledCallbacks(t *testing.T) {
	published := 0
	l := New(Options{
		Scan: fixedRows(ports.ListeningPort{Port: 3000, PID: 7, Process: "app"}),
	})
	l.SetDemand(func() (int, Include) { return 1, Include{Stats: true} })
	l.SetDemand(nil)
	include, scanNow, finished := l.waitForDemand(context.Background())
	if finished || !scanNow || !include.Stats {
		t.Fatalf("demand after a nil install = %+v scanNow=%v finished=%v", include, scanNow, finished)
	}
	l.SetPublisher(func(state.Snapshot, state.Snapshot, []state.Event) { published++ })
	l.SetPublisher(nil)
	l.scanAndPublish(Include{})
	if published != 1 {
		t.Fatalf("published %d times, want the callback installed before the nil call", published)
	}
}
