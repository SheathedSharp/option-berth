package scanner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestFailedScanDoesNotRenewCacheFreshness(t *testing.T) {
	now := time.Now()
	failure := false
	calls := 0
	l := New(Options{Now: func() time.Time { return now }, Scan: func(Include) ([]ports.ListeningPort, error) {
		calls++
		if failure {
			return nil, errors.New("collector unavailable")
		}
		return []ports.ListeningPort{{Port: 8080, PID: 42}}, nil
	}})
	l.scanAndPublish(Include{})
	good := l.Cached()
	observed := l.Status().LastScanAt
	now = now.Add(CacheTTL + time.Second)
	failure = true
	l.scanAndPublish(Include{})
	if !l.Status().LastScanAt.Equal(observed) {
		t.Fatal("failure renewed successful observation time")
	}
	if _, fresh := l.cached(Include{}); fresh {
		t.Fatal("failed scan made old snapshot fresh")
	}
	if got := l.dueIn(); got <= 0 {
		t.Fatalf("failure causes busy retry loop: %v", got)
	}
	if got := l.Cached(); got.Seq != good.Seq || got.At != good.At {
		t.Fatal("failure changed published observation")
	}
	// A read must retry rather than silently regard old evidence as current.
	_, _ = l.Snapshot(Include{})
	if calls != 3 {
		t.Fatalf("read did not retry failed observation: %d", calls)
	}
	failure = false
	l.scanAndPublish(Include{})
	if _, fresh := l.cached(Include{}); !fresh || l.Status().LastError != nil {
		t.Fatal("successful recovery not fresh")
	}
}

func TestOlderFailedScanCannotOverrideNewerSuccess(t *testing.T) {
	var calls atomic.Int32
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return nil, errors.New("older failure")
		}
		return []ports.ListeningPort{{Port: 8080, PID: 42}}, nil
	}})
	go func() { defer close(done); _, _ = l.Rescan(Include{}) }()
	<-started
	l.scanAndPublish(Include{})
	close(release)
	<-done
	if l.Status().LastError != nil {
		t.Fatalf("old failure replaced new successful outcome: %v", l.Status().LastError)
	}
	if _, fresh := l.cached(Include{}); !fresh {
		t.Fatal("old failure invalidated newer evidence")
	}
}

func TestCancelledLoopDoesNotWaitForScanGate(t *testing.T) {
	var calls atomic.Int32
	l := New(Options{Demand: func() (int, Include) { return 1, Include{} }, Scan: func(Include) ([]ports.ListeningPort, error) { calls.Add(1); return nil, nil }})
	lock(l.runGate)
	defer unlock(l.runGate)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); l.Run(ctx) }()
	cancel()
	l.Wake()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled loop waits for collector gate")
	}
	if calls.Load() != 0 {
		t.Fatal("cancelled loop started a new collector")
	}
}

func TestCarriedObservationsRequireSameRuntime(t *testing.T) {
	at := "2026-01-01T00:00:00Z"
	later := "2026-01-01T00:00:01Z"
	original := state.Port{Port: 8080, PID: 42, StartedAt: &at, Run: &state.Run{ID: "one"}, Health: &state.Health{Status: "ok", Configured: true}, Stats: &state.Stats{MemoryRSS: 1024}}
	for _, kind := range []string{"same", "pid", "start", "run", "container", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			prev := original
			next := original
			next.Health = nil
			next.Stats = nil
			switch kind {
			case "pid":
				next.PID = 43
			case "start":
				next.StartedAt = &later
			case "run":
				next.Run = &state.Run{ID: "two"}
			case "container":
				next.Docker = &state.Docker{Container: "new"}
			case "unknown":
				prev.PID = 0
				next.PID = 0
			}
			rows := []state.Port{next}
			carryHealth([]state.Port{prev}, rows, true)
			carryStats([]state.Port{prev}, rows)
			if (rows[0].Health != nil) != (kind == "same") || (rows[0].Stats != nil) != (kind == "same") {
				t.Fatalf("carried observations for %s: %+v", kind, rows[0])
			}
		})
	}
}

func TestSupersededBackgroundFailureDoesNotPublishAfterRecovery(t *testing.T) {
	var calls atomic.Int32
	var errorsPublished atomic.Int32
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	l := New(Options{
		Scan: func(Include) ([]ports.ListeningPort, error) {
			if calls.Add(1) == 1 {
				close(started)
				<-release
				return nil, errors.New("obsolete failure")
			}
			return []ports.ListeningPort{{Port: 8080, PID: 42}}, nil
		},
		Publish: func(_, _ state.Snapshot, events []state.Event) {
			for _, event := range events {
				if event.Kind == "scan_error" {
					errorsPublished.Add(1)
				}
			}
		},
	})
	go func() { defer close(done); l.scanAndPublish(Include{}) }()
	<-started
	_, err := l.Rescan(Include{})
	close(release)
	<-done
	if err != nil || errorsPublished.Load() != 0 {
		t.Fatalf("recovery err=%v obsolete errors=%d", err, errorsPublished.Load())
	}
}

func TestFailedObservationPreventsReadCoalescing(t *testing.T) {
	l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) { return nil, nil }})
	l.scanAndPublish(Include{})
	if _, ok := l.scannedSince(0, Include{}); !ok {
		t.Fatal("fresh successful scan did not coalesce")
	}
	l.opts.Scan = func(Include) ([]ports.ListeningPort, error) { return nil, errors.New("unavailable") }
	l.scanAndPublish(Include{})
	if _, ok := l.scannedSince(0, Include{}); ok {
		t.Fatal("failure was hidden by read coalescing")
	}
}
