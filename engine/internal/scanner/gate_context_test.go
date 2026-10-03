package scanner

import (
	"context"
	"errors"
	"testing"
	"time"
)

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
