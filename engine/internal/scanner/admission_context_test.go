package scanner

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// Arm immediately before the admission under examination. Err checks do not
// signal this barrier: a synchronous Mutex.Lock in that path must fail the
// test, rather than passing because cancellation happened before it queued.
type admissionWitness struct {
	context.Context
	armed   atomic.Bool
	once    sync.Once
	entered chan struct{}
}

func (c *admissionWitness) Done() <-chan struct{} {
	if c.armed.Load() {
		c.once.Do(func() { close(c.entered) })
	}
	return c.Context.Done()
}

func cancelAtAdmission(t *testing.T, m *contextMutex, armNow bool, run func(*admissionWitness) error) {
	t.Helper()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &admissionWitness{Context: parent, entered: make(chan struct{})}
	ctx.armed.Store(armNow)
	m.Lock()
	var once sync.Once
	release := func() { once.Do(m.Unlock) }
	defer release()
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	select {
	case <-ctx.entered:
	case <-time.After(time.Second):
		cancel()
		release()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("operation did not return after releasing its barrier")
		}
		t.Fatal("operation did not enter cancellable admission")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("admission error = %v", err)
		}
	case <-time.After(time.Second):
		release()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("operation did not return after releasing its barrier")
		}
		t.Fatal("cancellation waited for the lock holder")
	}
	if len(m.gate) != 1 {
		t.Fatal("cancelled waiter released the holder's token")
	}
}

func TestContextMutexCancelledAdmissionRetainsOwner(t *testing.T) {
	var m contextMutex
	cancelAtAdmission(t, &m, true, func(ctx *admissionWitness) error {
		return m.LockContext(ctx)
	})
	if err := m.LockContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.LockContext(ctx); !errors.Is(err, context.Canceled) || len(m.gate) != 0 {
		t.Fatalf("pre-cancelled admission: err=%v tokens=%d", err, len(m.gate))
	}
}

func TestScanCommitAdmissionCancelsSuccessAndFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		name := "success"
		if failure {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) { return nil, nil }})
			before := l.Status()
			cancelAtAdmission(t, &l.commitMu, false, func(ctx *admissionWitness) error {
				if failure {
					l.opts.ScanContext = func(context.Context, Include) ([]ports.ListeningPort, error) {
						ctx.armed.Store(true)
						return nil, errors.New("collector failed")
					}
				} else {
					l.SetSessions(func([]state.Port) []state.SessionRecord {
						ctx.armed.Store(true)
						return nil
					})
				}
				_, _, _, err := l.scanLockedContext(ctx, Include{}, Include{}, true)
				return err
			})
			if after := l.Status(); !reflect.DeepEqual(before, after) {
				t.Fatalf("cancelled commit changed status: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestAttributionAndSessionsAdmissionCancel(t *testing.T) {
	for _, sessions := range []bool{false, true} {
		l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) { return nil, nil }})
		var calls atomic.Int32
		l.SetSessions(func([]state.Port) []state.SessionRecord { calls.Add(1); return nil })
		cancelAtAdmission(t, &l.attr.mu, true, func(ctx *admissionWitness) error {
			if sessions {
				_, err := l.sessionsContext(ctx, nil)
				return err
			}
			_, _, err := l.attributeContext(ctx, nil)
			return err
		})
		if l.attr.index != nil || calls.Load() != 0 {
			t.Fatal("cancelled admission started attribution or a session callback")
		}
	}
}

func TestStatsAdmissionCancelsBeforeSamplingAndCommit(t *testing.T) {
	for _, commit := range []bool{false, true} {
		l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{{Port: 4000, PID: 42, MemoryRSS: 100}}, nil
		}})
		if _, err := l.Rescan(Include{Stats: true}); err != nil {
			t.Fatal(err)
		}
		before := l.Cached()
		var calls atomic.Int32
		m := &l.statsMu
		if commit {
			m = &l.commitMu
		}
		cancelAtAdmission(t, m, !commit, func(ctx *admissionWitness) error {
			l.opts.SampleStats = func([]int) map[int]ports.ProcSample {
				calls.Add(1)
				ctx.armed.Store(true)
				return map[int]ports.ProcSample{42: {MemoryRSS: 900}}
			}
			l.sampleStatsContext(ctx, Include{Stats: true})
			return ctx.Err()
		})
		want := int32(0)
		if commit {
			want = 1
		}
		if calls.Load() != want || !reflect.DeepEqual(before, l.Cached()) {
			t.Fatalf("commit=%v calls=%d: cancelled sample changed the snapshot", commit, calls.Load())
		}
	}
}

func TestRepublishCancelledCommitInvalidatesSavedWrite(t *testing.T) {
	l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) {
		return []ports.ListeningPort{{Port: 4000, PID: 42}}, nil
	}})
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatal(err)
	}
	before, revision := l.Cached(), l.refreshRequested
	cancelAtAdmission(t, &l.commitMu, false, func(ctx *admissionWitness) error {
		l.SetSessions(func([]state.Port) []state.SessionRecord {
			ctx.armed.Store(true)
			return nil
		})
		return l.RepublishContext(ctx)
	})
	if !reflect.DeepEqual(before, l.Cached()) || !l.lastScanAt.IsZero() || l.refreshRequested != revision+1 {
		t.Fatal("failed republish committed rows or left saved-write facts fresh")
	}
	if err := l.Republish(); err != nil {
		t.Fatalf("independent follow-up could not progress: %v", err)
	}
}

func TestRepublishFallbackPropagatesCancellation(t *testing.T) {
	entered := make(chan struct{})
	l := New(Options{ScanContext: func(ctx context.Context, _ Include) ([]ports.ListeningPort, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- l.RepublishContext(ctx) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fallback did not enter collection")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("fallback lost the caller's cancellation")
	}
	if l.haveSnap || l.seq != 0 || l.refreshRequested == 0 {
		t.Fatal("cancelled fallback published or lost the pending write")
	}
}
