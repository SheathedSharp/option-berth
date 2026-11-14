package runner

import (
	"context"
	"encoding/json"
	"github.com/sheathedsharp/option-berth/engine/internal/attn"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"net/http"
	"net/http/httptest"
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

func TestEventTaskAssessesOnceCachesAndRevokesWithoutLeakingScope(t *testing.T) {
	status, artifact := runnerFixture(t)
	var modelCalls atomic.Int32
	var anomaly, revoked atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		probabilities := map[string]float64{}
		for _, option := range artifact.Options {
			probabilities[option.ID] = 0
		}
		probabilities["inspect_logs"] = 1
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "synthetic/model", "answers": map[string]any{
			"task_relevant": map[string]any{"type": "noul", "noul": .9}, "needs_human": map[string]any{"type": "noul", "noul": .1},
			"next_option": map[string]any{"type": "choice", "choice": "inspect_logs", "confidence": .9, "probabilities": probabilities},
		}})
	}))
	defer srv.Close()
	client := model.Client{Endpoint: srv.URL, Model: "synthetic/model", APIKey: "synthetic", HTTPClient: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	changes := make(chan state.StateChanged, 8)
	var closed atomic.Int32
	trigger, closeFeed := OpenChanges(ctx, "a", time.Millisecond, func(context.Context, uint64) (ChangeStream, error) {
		return ChangeStream{Changes: changes, Identity: "test", Close: func() { closed.Add(1) }}, nil
	})
	defer closeFeed()
	trigger.Reconcile = time.Minute
	trigger.Check = func() error {
		if revoked.Load() {
			return ErrConsentRevoked
		}
		return nil
	}
	observed := make(chan Output, 16)
	finished := make(chan SubscriptionStats, 1)
	read := func(context.Context, string, string) (Status, error) {
		if anomaly.Load() {
			return status, nil
		}
		var clear Status
		clear.Scope.Root = status.Scope.Root
		return clear, nil
	}
	go func() {
		stats, err := SubscribeWithStats(ctx, "fake", status.Scope.Root, 5*time.Millisecond, client, model.TaskContext{Task: "fix worker", AuthorizedOptions: authorizedFor(artifact)}, read, "task", func(out Output, _ time.Duration) { observed <- out }, func(any) error { return nil }, trigger)
		if err != nil {
			t.Error(err)
		}
		finished <- stats
	}()
	next := func() Output {
		select {
		case out := <-observed:
			return out
		case <-ctx.Done():
			t.Fatal("no observation")
			return Output{}
		}
	}
	if out := next(); out.Status != "clear" || modelCalls.Load() != 0 {
		t.Fatalf("idle %+v", out)
	}
	anomaly.Store(true)
	// Defensive scope filtering must not consume the selected source's sequence.
	changes <- state.StateChanged{Seq: 1, Source: &state.EventSource{WorktreeID: "b"}, Changed: []string{"service_changed"}}
	changes <- state.StateChanged{Seq: 1, Source: &state.EventSource{WorktreeID: "a"}, Changed: []string{"service_failed"}}
	if out := next(); out.Status != "continue" || !out.ModelCalled || out.Cached || out.WorktreeRoot != status.Scope.Root {
		t.Fatalf("first assessment %+v", out)
	}
	changes <- state.StateChanged{Seq: 2, Source: &state.EventSource{WorktreeID: "a"}, Changed: []string{"health_changed"}}
	if out := next(); !out.Cached || out.ModelCalled {
		t.Fatalf("cache %+v", out)
	}
	revoked.Store(true)
	if out := next(); out.Status != "unavailable" || out.Reason != model.ReasonDisabled {
		t.Fatalf("revocation %+v", out)
	}
	select {
	case stats := <-finished:
		if stats.ModelCalls != 1 || stats.CacheHits != 1 || stats.NoEventPolls != 1 || modelCalls.Load() != 1 {
			t.Fatalf("stats %+v calls %d", stats, modelCalls.Load())
		}
	case <-ctx.Done():
		t.Fatal("task did not stop")
	}
	closeFeed()
	if closed.Load() != 1 {
		t.Fatalf("owned socket closes %d", closed.Load())
	}
	stored, err := attn.Read(status.AttnPath)
	if err != nil || stored.Assessment == nil {
		t.Fatal("assessment not persisted", err)
	}
}
