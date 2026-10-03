package runsreg

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRegistryParentCacheAgeStartsBeforeRead(t *testing.T) {
	r := testRegistry()
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }
	calls := 0
	r.Parents = func() map[int]int {
		calls++
		if calls == 1 {
			now = now.Add(parentsTTL + time.Second)
		}
		return map[int]int{42: calls}
	}
	if got := r.parentTable()[42]; got != 1 {
		t.Fatalf("first observation = %d", got)
	}
	if got := r.parentTable()[42]; got != 2 || calls != 2 {
		t.Fatalf("slow read renewed freshness: parent=%d calls=%d", got, calls)
	}
}

func TestRegistryParentCacheFutureTimeForcesRead(t *testing.T) {
	r := testRegistry()
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }
	r.parents, r.parentsAt = map[int]int{42: 1}, now.Add(time.Hour)
	r.Parents = func() map[int]int { return map[int]int{42: 2} }
	if got := r.parentTable()[42]; got != 2 || !r.parentsAt.Equal(now) {
		t.Fatalf("future evidence remained fresh: %d", got)
	}
}

func TestRegistryParentCacheHitReusesObservation(t *testing.T) {
	r := testRegistry()
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }
	calls := 0
	r.Parents = func() map[int]int {
		calls++
		return map[int]int{42: 7}
	}
	r.parentTable()
	ticket := r.parentsSeq
	now = now.Add(parentsTTL - time.Nanosecond)
	if got := r.parentTable()[42]; got != 7 || calls != 1 || r.parentsSeq != ticket {
		t.Fatalf("fresh hit collected: parent=%d calls=%d", got, calls)
	}
	now = now.Add(time.Nanosecond)
	r.parentTable()
	if calls != 2 {
		t.Fatal("TTL boundary did not refresh")
	}
}

func TestRegistryParentCacheDisabledReaderRejectsExpired(t *testing.T) {
	r := testRegistry()
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }
	r.parents, r.parentsAt = map[int]int{42: 1}, now.Add(-parentsTTL)
	r.Parents = nil
	if got := r.parentTable(); got != nil {
		t.Fatalf("disabled reader served stale evidence: %v", got)
	}
}

// Both observations have exactly the same timestamp. The barrier forces the
// old one to complete after the new one; timestamps cannot order this case.
func checkLateRegistryParentRead(t *testing.T, newer map[int]int) {
	t.Helper()
	r := testRegistry()
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var calls atomic.Int32
	r.Parents = func() map[int]int {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			return map[int]int{42: 1}
		}
		return newer
	}
	go func() { defer close(done); r.parentTable() }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("older reader did not enter")
	}
	r.parentTable()
	unblock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("older reader did not finish")
	}
	r.mu.Lock()
	table, accepted := r.parents, r.parentsCommitted
	r.mu.Unlock()
	if accepted != 2 {
		t.Fatalf("accepted ticket = %d, want 2", accepted)
	}
	if newer == nil {
		if table != nil {
			t.Fatalf("late result resurrected revoked evidence: %v", table)
		}
	} else if table[42] != newer[42] {
		t.Fatalf("late result overwrote newer observation: %v", table)
	}
}

func TestRegistryParentCacheLateReadCannotReplaceNewer(t *testing.T) {
	checkLateRegistryParentRead(t, map[int]int{42: 2})
}

func TestRegistryParentCacheEmptyNewerRevokesOlder(t *testing.T) {
	checkLateRegistryParentRead(t, nil)
}
