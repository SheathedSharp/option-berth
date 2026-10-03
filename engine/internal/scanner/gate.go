package scanner

import (
	"context"
	"errors"
	"time"
)

var errGateTimeout = errors.New("scanner gate wait budget exceeded")

// lock is retained for non-cancellable internal operations and test barriers.
func lock(gate chan struct{})   { gate <- struct{}{} }
func unlock(gate chan struct{}) { <-gate }

// lockContext does not leave a worker waiting after the caller departs. A
// cancellation racing with admission releases the token before returning.
func lockContext(ctx context.Context, gate chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			unlock(gate)
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func lockFor(gate chan struct{}, budget time.Duration) bool {
	return lockForContext(context.Background(), gate, budget) == nil
}

// lockForContext retains the gate's own budget as a distinct failure. An
// uncontended gate allocates no timer; a queued caller owns only one timer.
func lockForContext(ctx context.Context, gate chan struct{}, budget time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			unlock(gate)
			return err
		}
		return nil
	default:
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			unlock(gate)
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		if err := ctx.Err(); err != nil {
			return err
		}
		return errGateTimeout
	}
}
