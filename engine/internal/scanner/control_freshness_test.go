package scanner

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestRescanRejectsFailedObservationWithWarmCache(t *testing.T) {
	unavailable := errors.New("collector unavailable")
	fail := false
	l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) {
		if fail {
			return nil, unavailable
		}
		return []ports.ListeningPort{{Port: 8080, PID: 42}}, nil
	}})
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatal(err)
	}
	good := l.Cached()
	fail = true
	if _, err := l.Rescan(Include{}); !errors.Is(err, unavailable) {
		t.Fatalf("forced scan hid collector failure: %v", err)
	}
	if cached := l.Cached(); cached.Seq != good.Seq || cached.At != good.At {
		t.Fatal("failed control scan destroyed last good read state")
	}
	if got, err := l.Snapshot(Include{}); err != nil || got.Seq != good.Seq {
		t.Fatalf("read fallback changed: %+v, %v", got, err)
	}
}

// Gate the diagnostic logger to exercise the interval between observing a
// failure and publishing its notification, without sleeps or OS processes.
type blockedScanLog struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockedScanLog) Enabled(context.Context, slog.Level) bool { return true }
func (h *blockedScanLog) Handle(_ context.Context, _ slog.Record) error {
	h.once.Do(func() { close(h.entered); <-h.release })
	return nil
}
func (h *blockedScanLog) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *blockedScanLog) WithGroup(string) slog.Handler      { return h }

func TestRecoveryBetweenFailureAndNotificationDoesNotEmitStaleError(t *testing.T) {
	handler := &blockedScanLog{entered: make(chan struct{}), release: make(chan struct{})}
	var calls, reported atomic.Int32
	l := New(Options{Logger: slog.New(handler), Scan: func(Include) ([]ports.ListeningPort, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("old collector failure")
		}
		return []ports.ListeningPort{{Port: 8080, PID: 42}}, nil
	}, Publish: func(_, _ state.Snapshot, events []state.Event) {
		for _, e := range events {
			if e.Kind == "scan_error" {
				reported.Add(1)
			}
		}
	}})
	done := make(chan struct{})
	go func() { defer close(done); l.scanAndPublish(Include{}) }()
	<-handler.entered
	_, err := l.Rescan(Include{})
	close(handler.release)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if reported.Load() != 0 {
		t.Fatalf("old scan_error arrived after successful recovery: %d", reported.Load())
	}
}

func TestSnapshotDoesNotAcceptFutureObservationTime(t *testing.T) {
	now := time.Now()
	l := New(Options{Now: func() time.Time { return now }, Scan: func(Include) ([]ports.ListeningPort, error) { return nil, nil }})
	l.scanAndPublish(Include{})
	now = now.Add(-time.Second)
	if _, ok := l.cached(Include{}); ok {
		t.Fatal("clock rollback made future observation fresh")
	}
}
