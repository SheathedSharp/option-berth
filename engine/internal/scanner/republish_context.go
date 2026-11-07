package scanner

import (
	"context"
	"errors"
	"fmt"
)

// RepublishContext observes a saved write with the caller's cancellation and
// deadline. Cancellation cannot undo that write, nor a snapshot already
// committed: once commit starts, ordered publication completes synchronously.
// Before that point, an error invalidates the cache rather than acknowledging
// stale facts as fresh. The existing Republish entry owns a bounded follow-up
// independent of the request that performed the write.
func (l *Loop) RepublishContext(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			l.Invalidate()
		} else {
			l.Wake()
		}
	}()
	if err := lockForContext(ctx, l.orderGate, ScanLockBudget); err != nil {
		return republishWaitError(err)
	}
	return l.republishHeld(ctx)
}

func republishWaitError(err error) error {
	if errors.Is(err, errGateTimeout) {
		return fmt.Errorf("scanner busy for more than %s; the change is saved and the next scan will publish it", ScanLockBudget)
	}
	return err
}

// republishHeld re-attributes the ports the last scan already read. An empty
// cache still has to observe the machine, so it releases the order gate and
// falls through to a rescan. The gate stays held for the attribute, probe
// and commit so a later sequence cannot pass this one.
func (l *Loop) republishHeld(ctx context.Context) error {
	l.mu.Lock()
	pp, have, narrow := l.lastPorts, l.haveSnap, l.lastNarrow
	l.mu.Unlock()
	if !have || len(pp) == 0 {
		unlock(l.orderGate)
		_, err := l.RescanContext(ctx, Include{})
		return err
	}
	defer unlock(l.orderGate)

	subs, carry := l.opts.Demand()
	rows, groupRows, err := l.attributeContext(ctx, pp, narrow)
	if err != nil {
		return err
	}
	sessionRows, err := l.sessionsContext(ctx, rows)
	if err != nil {
		return err
	}
	if err := ProbeConfiguredContext(ctx, rows, groupRows, l.ProbeContext, ConfiguredHealthBudget); err != nil {
		return err
	}
	if err := l.commitMu.LockContext(ctx); err != nil {
		return err
	}
	defer l.commitMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	next, prev, changed := l.commitRepublish(subs, carry, rows, groupRows, sessionRows)
	if changed {
		l.publish(prev, next, deriveEvents(prev, next, l.nowRFC3339()))
	}
	return nil
}
