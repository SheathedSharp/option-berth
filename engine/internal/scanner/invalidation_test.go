package scanner

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
)

func TestInvalidationBypassesCadenceButReadWakeDoesNot(t *testing.T) {
	now := time.Now()
	l := New(Options{Now: func() time.Time { return now }, Scan: func(Include) ([]ports.ListeningPort, error) { return nil, nil }})
	l.scanAndPublish(Include{})
	l.Wake()
	if l.dueIn() <= 0 {
		t.Fatal("ordinary read wake scheduled a redundant scan")
	}
	l.Invalidate()
	if l.dueIn() > 0 {
		t.Fatal("a known change still waits behind the old scan cadence")
	}
	l.scanAndPublish(Include{})
	if l.dueIn() <= 0 {
		t.Fatal("completed invalidation causes a scan spin")
	}
}

// Release the scan only after the change: a completion timestamp cannot prove
// that its OS observation began after invalidation. Keep the clock frozen.
func TestPreInvalidationScanCannotRenewFreshness(t *testing.T) {
	now := time.Now()
	var calls atomic.Int32
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	l := New(Options{Now: func() time.Time { return now }, Scan: func(Include) ([]ports.ListeningPort, error) {
		if calls.Add(1) == 2 {
			close(entered)
			<-release
		}
		return []ports.ListeningPort{{Port: 8080, PID: 42}}, nil
	}})
	l.scanAndPublish(Include{})
	go func() { defer close(done); _, _ = l.Rescan(Include{}) }()
	t.Cleanup(func() { unblock(); <-done })
	<-entered
	l.Invalidate()
	unblock()
	<-done
	if _, fresh := l.cached(Include{}); fresh {
		t.Fatal("pre-invalidation scan incorrectly renewed freshness")
	}
	if _, fresh := l.scannedSince(1, Include{}); fresh {
		t.Fatal("pre-invalidation scan incorrectly discharged a waiting read")
	}
	if l.dueIn() > 0 {
		t.Fatal("pre-invalidation completion suppressed pending refresh")
	}
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("next read did not observe the change: calls=%d", calls.Load())
	}
	if _, fresh := l.cached(Include{}); !fresh {
		t.Fatal("post-invalidation observation not reusable")
	}
}

func TestInvalidationsCoalesceAndFailedAttemptKeepsRetryPaced(t *testing.T) {
	now := time.Now()
	fail, calls := false, 0
	l := New(Options{Now: func() time.Time { return now }, Scan: func(Include) ([]ports.ListeningPort, error) {
		calls++
		if fail {
			return nil, errors.New("collector failed")
		}
		return nil, nil
	}})
	l.scanAndPublish(Include{})
	for i := 0; i < 100; i++ {
		l.Invalidate()
	}
	fail = true
	l.scanAndPublish(Include{})
	if calls != 2 || l.dueIn() <= 0 {
		t.Fatalf("failed batch busy-retries: calls=%d due=%s", calls, l.dueIn())
	}
	if _, fresh := l.cached(Include{}); fresh {
		t.Fatal("failed observation served as fresh")
	}
	// A genuinely NEW change may trigger one new attempt, even after failure.
	l.Invalidate()
	if l.dueIn() > 0 {
		t.Fatal("new change hidden by failed attempt")
	}
	fail = false
	l.scanAndPublish(Include{})
	if l.dueIn() <= 0 {
		t.Fatal("coalesced changes not discharged")
	}
	if _, fresh := l.cached(Include{}); !fresh {
		t.Fatal("recovery not fresh")
	}
}

func TestNewerObservationDischargesChangesDespiteOlderCompletion(t *testing.T) {
	var calls atomic.Int32
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil, nil
	}})
	go func() { defer close(done); _, _ = l.Rescan(Include{}) }()
	t.Cleanup(func() { unblock(); <-done })
	<-entered
	l.Invalidate()
	l.scanAndPublish(Include{})
	unblock()
	<-done
	if _, fresh := l.cached(Include{}); !fresh {
		t.Fatal("older completion invalidated new observation")
	}
	if l.dueIn() <= 0 {
		t.Fatal("older completion resurrected discharged changes")
	}
}

// Drive the real subscribed loop with a frozen observation clock. It must
// process known changes without waiting for ordinary cache age to advance.
func TestSubscribedLoopProcessesPendingInvalidation(t *testing.T) {
	now := time.Now()
	observed := make(chan struct{}, 4)
	l := New(Options{
		Now:    func() time.Time { return now },
		Demand: func() (int, Include) { return 1, Include{} },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			observed <- struct{}{}
			return nil, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); l.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("subscribed loop did not stop")
		}
	})
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("first observation did not run")
	}
	l.Invalidate()
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("subscribed loop delayed invalidation behind frozen idle age")
	}
}
