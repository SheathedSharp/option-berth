package scanner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// These regressions deliberately use APIs already present before the context
// seams were added, so an old-code behavioral failure is not a compile error.
func TestCancelledLegacyScanDoesNotCommit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			phase, publications := 0, 0
			l := New(Options{
				Scan: func(Include) ([]ports.ListeningPort, error) {
					if phase == 1 {
						cancel()
						if fail {
							return nil, errors.New("cancelled legacy collection")
						}
					}
					return []ports.ListeningPort{{Port: 8080 + phase, PID: 123, BindAddress: "127.0.0.1"}}, nil
				},
				Publish: func(state.Snapshot, state.Snapshot, []state.Event) { publications++ },
			})
			if _, err := l.Rescan(Include{}); err != nil {
				t.Fatal(err)
			}
			l.Invalidate()
			before, published := l.Status(), publications
			phase = 1
			l.scanAndPublishContext(ctx, Include{})
			after := l.Status()
			if after.Seq != before.Seq || after.Scans != before.Scans || after.LastError != before.LastError || after.LastScanAt != before.LastScanAt || publications != published {
				t.Fatalf("cancelled scan changed outcome: before=%+v after=%+v publications=%d->%d", before, after, published, publications)
			}
			if _, ok := l.cached(Include{}); ok {
				t.Fatal("cancelled scan consumed pending invalidation")
			}
			phase = 2
			if _, err := l.Rescan(Include{}); err != nil {
				t.Fatal(err)
			}
			if _, ok := l.cached(Include{}); !ok {
				t.Fatal("later observation could not recover")
			}
		})
	}
}

func TestCancelledScanAtOrderGateDoesNotPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	collected := make(chan struct{})
	published := 0
	l := New(Options{
		Scan:    func(Include) ([]ports.ListeningPort, error) { close(collected); return nil, nil },
		Publish: func(state.Snapshot, state.Snapshot, []state.Event) { published++ },
	})
	lock(l.orderGate)
	done := make(chan struct{})
	go func() { l.scanAndPublishContext(ctx, Include{}); close(done) }()
	select {
	case <-collected:
	case <-time.After(time.Second):
		unlock(l.orderGate)
		t.Fatal("collector did not run")
	}
	cancel()
	unlock(l.orderGate)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled ordered scan did not join")
	}
	if published != 0 || l.Status().Seq != 0 {
		t.Fatal("cancelled scan published after order gate")
	}
}

func TestRunJoinsCancelledLegacyScanWithoutPublishing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	published := 0
	l := New(Options{
		Demand:  func() (int, Include) { return 1, Include{} },
		Scan:    func(Include) ([]ports.ListeningPort, error) { close(entered); <-release; return nil, nil },
		Publish: func(state.Snapshot, state.Snapshot, []state.Event) { published++ },
	})
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("scan did not start")
	}
	cancel()
	select {
	case <-done:
		close(release)
		t.Fatal("legacy callback detached before returning")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not join")
	}
	if published != 0 || l.Status().Seq != 0 {
		t.Fatal("Run published cancelled legacy result")
	}
}
