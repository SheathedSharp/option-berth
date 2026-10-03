package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type groupWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *groupWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}
func lockCount(r *Runtime) int {
	r.groupLocksMu.Lock()
	defer r.groupLocksMu.Unlock()
	return len(r.groupLocks)
}

func TestGroupAdmissionIsolationAndReclamation(t *testing.T) {
	r := &Runtime{}
	release, err := r.AcquireGroup(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	other, err := r.AcquireGroup(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	other()
	if lockCount(r) != 1 {
		t.Fatal("idle group retained or active group removed")
	}
	release()
	if lockCount(r) != 0 {
		t.Fatal("last owner retained group name")
	}
}

func TestGroupAdmissionPreCancelledDoesNotAllocateEntry(t *testing.T) {
	r := &Runtime{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release, err := r.AcquireGroup(ctx, "cancelled")
	if release != nil || !errors.Is(err, context.Canceled) || lockCount(r) != 0 {
		t.Fatalf("release=%v error=%v count=%d", release != nil, err, lockCount(r))
	}
}

func TestGroupAdmissionCancelledWaiterRetainsOwner(t *testing.T) {
	r := &Runtime{}
	release, err := r.AcquireGroup(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &groupWaitContext{Context: parent, entered: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		unlock, err := r.AcquireGroup(ctx, "a")
		if unlock != nil {
			unlock()
		}
		done <- err
	}()
	select {
	case <-ctx.entered:
	case <-time.After(time.Second):
		t.Fatal("waiter not enrolled")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter waited for owner")
	}
	r.groupLocksMu.Lock()
	l := r.groupLocks["a"]
	intact := l != nil && l.users == 1 && len(l.held) == 1
	r.groupLocksMu.Unlock()
	if !intact {
		t.Fatal("cancelled waiter changed owner's token or reference")
	}
}

func TestGroupAdmissionDeadlineAndLateRelease(t *testing.T) {
	r := &Runtime{}
	first, err := r.AcquireGroup(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	unlock, err := r.AcquireGroup(ctx, "a")
	if unlock != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline=%v", err)
	}
	first()
	second, err := r.AcquireGroup(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	first() // a stale/idempotent callback must not delete a newly created lock
	r.groupLocksMu.Lock()
	l := r.groupLocks["a"]
	intact := l != nil && l.users == 1 && len(l.held) == 1
	r.groupLocksMu.Unlock()
	if !intact {
		t.Fatal("late release affected replacement owner")
	}
}

func TestGroupAdmissionContentionRemainsExclusive(t *testing.T) {
	r := &Runtime{}
	var active, completed atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				release, err := r.AcquireGroup(context.Background(), "shared")
				if err != nil {
					t.Error(err)
					return
				}
				if active.Add(1) != 1 {
					t.Error("overlapping lifecycle owners")
				}
				completed.Add(1)
				active.Add(-1)
				release()
			}
		}()
	}
	wg.Wait()
	if completed.Load() != 320 || active.Load() != 0 || lockCount(r) != 0 {
		t.Fatalf("completed=%d active=%d locks=%d", completed.Load(), active.Load(), lockCount(r))
	}
}

func TestGroupAdmissionCancellationRaceReturnsReferences(t *testing.T) {
	r := &Runtime{}
	for range 64 {
		release, err := r.AcquireGroup(context.Background(), "shared")
		if err != nil {
			t.Fatal(err)
		}
		parent, cancel := context.WithCancel(context.Background())
		ctx := &groupWaitContext{Context: parent, entered: make(chan struct{})}
		done := make(chan error, 1)
		go func() {
			unlock, err := r.AcquireGroup(ctx, "shared")
			if unlock != nil {
				unlock()
			}
			done <- err
		}()
		select {
		case <-ctx.entered:
		case <-time.After(time.Second):
			cancel()
			release()
			t.Fatal("waiter not enrolled")
		}
		cancelled := make(chan struct{})
		go func() { cancel(); close(cancelled) }()
		release()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("admission race stranded waiter")
		}
		<-cancelled
		if lockCount(r) != 0 {
			t.Fatal("race retained group")
		}
	}
}

func TestGroupAdmissionChurnDoesNotRetainHistory(t *testing.T) {
	r := &Runtime{}
	// The old stable-pointer/zero-allocation lookup contract retained every
	// historical name. The replacement keeps identity for active operations.
	for i := range 10000 {
		release, err := r.AcquireGroup(context.Background(), fmt.Sprintf("worktree-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	if lockCount(r) != 0 {
		t.Fatal("idle worktree history retained")
	}
}
