package scanner

import (
	"context"
	"github.com/sheathedsharp/option-berth/engine/internal/runstate"
	"github.com/sheathedsharp/option-berth/engine/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"time"
)

// RuntimeChanged is a bounded wakeup, not another registry or OS observation.
func (l *Loop) RuntimeChanged() { nudge(l.runtimeWake); l.Wake() }
func (l *Loop) runRuntime(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.runtimeWake:
			if _, _, err := l.RefreshRuntimeContext(ctx); err != nil && ctx.Err() == nil {
				l.opts.Logger.Warn("refreshing runtime facts", "error", err)
			}
		}
	}
}
func (l *Loop) projectRuntime(ctx context.Context, snap state.Snapshot, fresh bool) (state.Snapshot, bool, error) {
	if err := l.attr.mu.LockContext(ctx); err != nil {
		return snap, false, err
	}
	reg := l.registry()
	l.attr.mu.Unlock()
	provider, ok := reg.(servicefacts.Provider)
	if !ok {
		return snap, false, nil
	}
	return runstate.Project(snap, provider.ServiceFacts(), fresh), true, nil
}

// RefreshRuntimeContext publishes registered run progress without entering the
// OS/health ordering gate. Listener age remains independent and explicit.
func (l *Loop) RefreshRuntimeContext(ctx context.Context) (state.Snapshot, bool, error) {
	if err := l.commitMu.LockContext(ctx); err != nil {
		return state.Snapshot{}, false, err
	}
	defer l.commitMu.Unlock()
	l.mu.Lock()
	previous, have, at := l.snap, l.haveSnap, l.lastScanAt
	l.mu.Unlock()
	if !have {
		return previous, false, nil
	}
	age := l.now().Sub(at)
	fresh := age >= 0 && age < CacheTTL
	next, provided, err := l.projectRuntime(ctx, previous, fresh)
	if err != nil || !provided {
		return previous, false, err
	}
	if err = ctx.Err(); err != nil {
		return previous, false, err
	}
	if !snapshotChanged(previous, next, true) {
		return previous, true, nil
	}
	l.mu.Lock()
	l.seq++
	next.Seq = l.seq
	next.At = l.now().Format(time.RFC3339)
	l.snap = next
	l.mu.Unlock()
	// Runtime changes are derived by the shared group delta. A stale/unknown
	// listener table must never manufacture port_down history.
	l.publish(previous, next, nil)
	return next, true, nil
}
