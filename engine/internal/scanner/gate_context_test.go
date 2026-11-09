package scanner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// cancelOnCall reports cancellation from the given Err call onward. It models
// the window the post-acquisition guard exists for: the caller still owned the
// call while the token was being taken, and the withdrawal lands before the
// caller gets to use it. A real context cannot be ordered that precisely, and a
// sleep would only guess at the window.
type cancelOnCall struct {
	context.Context
	calls atomic.Int32
	at    int32
}

func (c *cancelOnCall) Err() error {
	if c.calls.Add(1) >= c.at {
		return context.Canceled
	}
	return nil
}

// TestGateReturnsTheTokenWhenWithdrawnAfterAcquisition is the guard's other
// half: the token is taken while the caller still owns the call, and the
// withdrawal lands before the caller can use it. The token belongs back on the
// gate; a leak here blocks every later scan.
func TestGateReturnsTheTokenWhenWithdrawnAfterAcquisition(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		gate := make(chan struct{}, 1)
		// The entry check is the first Err call; the check with the token held
		// is the second.
		ctx := &cancelOnCall{Context: context.Background(), at: 2}
		var err error
		if bounded {
			err = lockForContext(ctx, gate, time.Second)
		} else {
			err = lockContext(ctx, gate)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("bounded=%v err=%v, want the caller's cancellation", bounded, err)
		}
		if len(gate) != 0 {
			t.Fatalf("bounded=%v token stayed taken after the caller was withdrawn", bounded)
		}
	}
}

// TestGateTimeoutYieldsToCancellation: when the budget runs out at the same
// moment the caller leaves, the answer is the cancellation, not a spent
// budget. The caller is gone; it did not spend anything.
func TestGateTimeoutYieldsToCancellation(t *testing.T) {
	gate := make(chan struct{}, 1)
	lock(gate)
	defer unlock(gate)
	// Entry check first; the check after the timer fires is the second.
	ctx := &cancelOnCall{Context: context.Background(), at: 2}
	if err := lockForContext(ctx, gate, 5*time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the caller's cancellation", err)
	}
}

func TestAdmitReleasesTheTokenWhenWithdrawnAfterAcquisition(t *testing.T) {
	l := New(Options{})
	// lockContext checks at entry and again with the token held; the third
	// check is admit's own, the one that runs after acquisition returned.
	release, admitted := l.admit(&cancelOnCall{Context: context.Background(), at: 3})
	if admitted || release != nil {
		t.Fatalf("admitted=%v release=%v, a withdrawn caller was admitted", admitted, release != nil)
	}
	if len(l.runGate) != 0 {
		t.Fatal("the token stayed taken after the caller was withdrawn")
	}
}

func TestAdmitReturnsAReleaseForALiveCaller(t *testing.T) {
	l := New(Options{})
	release, admitted := l.admit(context.Background())
	if !admitted || release == nil {
		t.Fatalf("admitted=%v release=%v, a live caller was refused", admitted, release != nil)
	}
	if len(l.runGate) != 1 {
		t.Fatal("an admitted caller does not hold the token")
	}
	release()
	if len(l.runGate) != 0 {
		t.Fatal("releasing an admitted call left the token behind")
	}
}

func TestGateContextPreCancelledNeverConsumesToken(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		gate := make(chan struct{}, 1)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		for range 1000 {
			var err error
			if bounded {
				err = lockForContext(ctx, gate, time.Second)
			} else {
				err = lockContext(ctx, gate)
			}
			if !errors.Is(err, context.Canceled) || len(gate) != 0 {
				t.Fatalf("bounded=%v err=%v tokens=%d", bounded, err, len(gate))
			}
		}
	}
}

func TestGateContextCancelsHeldGate(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		gate := make(chan struct{}, 1)
		lock(gate)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			if bounded {
				done <- lockForContext(ctx, gate, time.Hour)
			} else {
				done <- lockContext(ctx, gate)
			}
		}()
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("gate wait ignored cancellation")
		}
		if len(gate) != 1 {
			t.Fatal("waiter released someone else's token")
		}
		unlock(gate)
	}
}

func TestGateContextRetainsIndependentBudget(t *testing.T) {
	gate := make(chan struct{}, 1)
	lock(gate)
	if err := lockForContext(context.Background(), gate, time.Millisecond); !errors.Is(err, errGateTimeout) {
		t.Fatalf("budget error=%v", err)
	}
	unlock(gate)
	if err := lockForContext(context.Background(), gate, 0); err != nil {
		t.Fatal(err)
	}
	unlock(gate)
}
