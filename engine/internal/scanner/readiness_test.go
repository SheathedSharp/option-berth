package scanner

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
)

func TestReadinessUsesActiveAgeWithoutChangingIdleTTL(t *testing.T) {
	now := time.Now()
	calls := 0
	l := New(Options{Now: func() time.Time { return now }, Scan: func(Include) ([]ports.ListeningPort, error) { calls++; return nil, nil }})
	l.scanAndPublish(Include{})
	now = now.Add(MinScanInterval)
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("ordinary read no longer uses idle TTL")
	}
	if _, err := l.SnapshotForReadiness(); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("active dependency reused older idle observation: %d", calls)
	}
	if l.Status().BaseIntervalMs != int(BaseInterval/time.Millisecond) {
		t.Fatal("active request changed global scan floor")
	}
}

func TestReadinessConcurrentWaitersShareFreshObservation(t *testing.T) {
	now := time.Now()
	calls := 0
	l := New(Options{Now: func() time.Time { return now }, Scan: func(Include) ([]ports.ListeningPort, error) { calls++; return nil, nil }})
	l.scanAndPublish(Include{})
	now = now.Add(CacheTTL + time.Second)
	lock(l.rpcGate)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := l.scanNow(Include{}, readinessScan); err != nil {
				t.Error(err)
			}
		}()
	}
	unlock(l.rpcGate)
	wg.Wait()
	if calls != 2 {
		t.Fatalf("32 active waiters paid for %d extra scans, want 1", calls-1)
	}
	// Control requests must NEVER inherit this age-based coalescing policy.
	for i := 0; i < 3; i++ {
		if _, err := l.Rescan(Include{}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 5 {
		t.Fatal("control rescan was incorrectly coalesced")
	}
}

func TestReadinessFailsClosedWhenObservationFails(t *testing.T) {
	now := time.Now()
	fail := false
	boom := errors.New("no fresh OS evidence")
	l := New(Options{Now: func() time.Time { return now }, Scan: func(Include) ([]ports.ListeningPort, error) {
		if fail {
			return nil, boom
		}
		return []ports.ListeningPort{{Port: 8080, PID: 42}}, nil
	}})
	l.scanAndPublish(Include{})
	now = now.Add(MinScanInterval)
	fail = true
	snap, err := l.SnapshotForReadiness()
	if !errors.Is(err, boom) || len(snap.Ports) != 0 {
		t.Fatalf("stale dependency accepted after failure: %+v %v", snap, err)
	}
	if len(l.Cached().Ports) != 1 {
		t.Fatal("read-only last-good evidence was destroyed")
	}
}

func TestReadinessFailedAttemptsAreSharedAndPaced(t *testing.T) {
	now := time.Now()
	calls := 0
	boom := errors.New("failed round")
	l := New(Options{Now: func() time.Time { return now }, Scan: func(Include) ([]ports.ListeningPort, error) { calls++; return nil, boom }})
	for i := 0; i < 32; i++ {
		if _, err := l.SnapshotForReadiness(); !errors.Is(err, boom) {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("failed readiness polls ran %d collectors instead of one", calls)
	}
	now = now.Add(MinScanInterval)
	_, _ = l.SnapshotForReadiness()
	if calls != 2 {
		t.Fatal("failed observation not retried at active cadence")
	}
	l.Invalidate()
	_, _ = l.SnapshotForReadiness()
	if calls != 3 {
		t.Fatal("new invalidation did not permit a new attempt")
	}
}
