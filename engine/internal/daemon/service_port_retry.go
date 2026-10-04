package daemon

import (
	"context"
	"errors"
	"github.com/sheathedsharp/option-berth/internal/scanner"
	"github.com/sheathedsharp/option-berth/internal/state"
	"time"
)

type retryableReservationRelease interface{ RetryableReservationRelease() bool }

func reservationReleaseBusy(err error) bool {
	var busy retryableReservationRelease
	return errors.As(err, &busy) && busy.RetryableReservationRelease()
}

// Retry only the final release observation after transient registry contention.
// All signals and the original reservation capture stay outside this loop.
// Each release attempt drops its locks before another observation is made.
func (p *servicePortRelease) releaseAfterContention(ctx context.Context, rt *Runtime, group string, after state.Snapshot, scanErr error, results []state.KillResult) (int, error) {
	n, err := p.release(ctx, rt, group, after, scanErr, results)
	if n != 0 || !reservationReleaseBusy(err) || rt.Scanner == nil {
		return n, err
	}
	retryCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for retry := 0; retry < 3; retry++ {
		select {
		case <-retryCtx.Done():
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			return 0, err
		case <-tick.C:
		}
		after, scanErr = rt.Scanner.RescanContext(retryCtx, scanner.Include{})
		n, err = p.release(retryCtx, rt, group, after, scanErr, results)
		if n != 0 || !reservationReleaseBusy(err) {
			return n, err
		}
	}
	return n, err
}
