package scanner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
)

func TestContextReadCancellationNeverUsesLastGood(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	phase := 0
	l := New(Options{
		Scan: func(Include) ([]ports.ListeningPort, error) {
			t.Fatal("legacy override took precedence")
			return nil, nil
		},
		ScanContext: func(context.Context, Include) ([]ports.ListeningPort, error) {
			if phase > 0 {
				cancel()
				return nil, errors.New("cancelled collection")
			}
			return []ports.ListeningPort{{Port: 18000, PID: 123, BindAddress: "127.0.0.1"}}, nil
		},
	})
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatal(err)
	}
	l.Invalidate()
	before := l.Status()
	phase = 1
	snap, err := l.SnapshotAllContext(ctx, Include{})
	if !errors.Is(err, context.Canceled) || snap.Seq != 0 {
		t.Fatalf("cancelled read used last-good: %+v %v", snap, err)
	}
	after := l.Status()
	if after.Seq != before.Seq || after.LastError != before.LastError {
		t.Fatalf("cancel changed state: before=%+v after=%+v", before, after)
	}
}

func TestContextSnapshotPreCancelledRejectsFreshCache(t *testing.T) {
	l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) { return nil, nil }})
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.SnapshotAllContext(ctx, Include{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("snapshot cancel=%v", err)
	}
	if _, err := l.SnapshotForReadinessContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("readiness cancel=%v", err)
	}
	if _, err := l.RescanContext(ctx, Include{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("rescan cancel=%v", err)
	}
}

func TestReadinessCancellationDoesNotPoisonOtherWaiter(t *testing.T) {
	entered := make(chan struct{})
	var calls atomic.Int32
	l := New(Options{ScanContext: func(ctx context.Context, _ Include) ([]ports.ListeningPort, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return nil, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { _, err := l.SnapshotForReadinessContext(ctx); first <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first read did not start")
	}
	go func() { _, err := l.SnapshotForReadinessContext(context.Background()); second <- err }()
	cancel()
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first did not join")
	}
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("independent waiter=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second did not join")
	}
	if calls.Load() != 2 || l.Status().LastError != nil {
		t.Fatalf("calls=%d status=%+v", calls.Load(), l.Status())
	}
}

func TestContextReadCancelsWhileRPCGateHeld(t *testing.T) {
	var calls atomic.Int32
	l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) { calls.Add(1); return nil, nil }})
	lock(l.rpcGate)
	defer unlock(l.rpcGate)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := l.SnapshotForReadinessContext(ctx); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("gate cancel=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RPC gate did not cancel")
	}
	if calls.Load() != 0 || len(l.rpcGate) != 1 {
		t.Fatalf("collector=%d tokens=%d", calls.Load(), len(l.rpcGate))
	}
}

func TestLoopProbeContextRetainsLegacyAndHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) { return nil, nil }, Probe: func(string, int, string, time.Duration) ports.HealthResult {
		calls++
		cancel()
		return ports.HealthResult{Status: "healthy"}
	}})
	got := l.ProbeContext(ctx, "127.0.0.1", 18000, "/", time.Second)
	if got != (ports.HealthResult{}) || calls != 1 {
		t.Fatalf("probe=%+v calls=%d", got, calls)
	}
}
