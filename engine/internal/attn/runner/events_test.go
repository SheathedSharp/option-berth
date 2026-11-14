package runner

import (
	"context"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"sync/atomic"
	"testing"
	"time"
)

func TestEventTriggerDoesNotPollFullStatusWhileIdleAndRevokes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	wake := make(chan struct{}, 1)
	var calls atomic.Int32
	var revoked atomic.Bool
	trigger := Trigger{Wake: wake, Connected: func() bool { return true }, Reconcile: time.Second, Check: func() error {
		if revoked.Load() {
			return ErrConsentRevoked
		}
		return nil
	}}
	done := make(chan error, 1)
	ready := make(chan struct{}, 1)
	go func() {
		_, err := SubscribeWithStats(ctx, "fake", "/worktree", 5*time.Millisecond, model.Client{}, model.TaskContext{}, func(context.Context, string, string) (Status, error) {
			calls.Add(1)
			var s Status
			s.Scope.Root = "/worktree"
			return s, nil
		}, "task", nil, func(v any) error {
			if o, ok := v.(Output); ok && o.Status == "clear" {
				select {
				case ready <- struct{}{}:
				default:
				}
			}
			return nil
		}, trigger)
		done <- err
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("no initial status")
	}
	timer := time.NewTimer(35 * time.Millisecond)
	<-timer.C
	if calls.Load() != 1 {
		t.Fatalf("idle full reads=%d", calls.Load())
	}
	revoked.Store(true)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("revocation waited for event")
	}
	if calls.Load() != 1 {
		t.Fatal("revocation caused another status read")
	}
}
func TestEventFeedScopeDedupReconnectAndJoin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, b := make(chan state.StateChanged, 8), make(chan state.StateChanged, 8)
	var connects, closed atomic.Int32
	var resume atomic.Uint64
	trigger, stop := OpenChanges(ctx, "a", time.Millisecond, func(ctx context.Context, after uint64) (ChangeStream, error) {
		n := connects.Add(1)
		ch, id := a, "old"
		if n > 1 {
			ch, id = b, "new"
			resume.Store(after)
		}
		return ChangeStream{Changes: ch, Identity: id, Close: func() { closed.Add(1) }}, nil
	})
	defer stop()
	send := func(ch chan state.StateChanged, seq uint64, wt, kind string) {
		ch <- state.StateChanged{Seq: seq, Source: &state.EventSource{WorktreeID: wt}, Changed: []string{kind}}
	}
	send(a, 1, "b", "service_changed")
	send(a, 2, "a", "stats_changed")
	send(a, 3, "a", "service_changed")
	select {
	case <-trigger.Wake:
	case <-ctx.Done():
		t.Fatal("no related wake")
	}
	send(a, 3, "a", "service_changed")
	select {
	case <-trigger.Wake:
		t.Fatal("duplicate wake")
	case <-time.After(10 * time.Millisecond):
	}
	close(a)
	select {
	case <-trigger.Wake:
	case <-ctx.Done():
		t.Fatal("no reconnect snapshot wake")
	}
	if resume.Load() != 3 {
		t.Fatal("cursor lost")
	}
	send(b, 1, "a", "health_changed")
	select {
	case <-trigger.Wake:
	case <-ctx.Done():
		t.Fatal("new daemon sequence not reset")
	}
	stop()
	if closed.Load() != 2 {
		t.Fatalf("owned stream closures=%d", closed.Load())
	}
}
func TestTriggerCalibrationAndOfflineFallback(t *testing.T) {
	for _, connected := range []bool{true, false} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		start := time.Now()
		trigger := Trigger{Wake: make(chan struct{}), Connected: func() bool { return connected }, Reconcile: 25 * time.Millisecond}
		if err := trigger.wait(ctx, 5*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		elapsed := time.Since(start)
		cancel()
		if connected && elapsed < 20*time.Millisecond {
			t.Fatal("ignored reconciliation budget")
		}
		if !connected && elapsed > 100*time.Millisecond {
			t.Fatal("offline poll fallback missing")
		}
	}
}
