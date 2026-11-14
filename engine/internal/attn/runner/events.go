package runner

import (
	"context"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"sync/atomic"
	"time"
)

// Trigger only schedules fact reads. It grants no action or model permission.
type Trigger struct {
	Wake      <-chan struct{}
	Connected func() bool
	Check     func() error
	Reconcile time.Duration
}

func (t Trigger) wait(ctx context.Context, interval time.Duration) error {
	reconcile := t.Reconcile
	if reconcile <= 0 {
		reconcile = max(10*time.Second, interval)
	}
	deadline := time.NewTimer(reconcile)
	defer deadline.Stop()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-t.Wake:
			if !ok {
				t.Wake = nil
				t.Connected = nil
				continue
			}
			if t.Check != nil {
				return t.Check()
			}
			return nil
		case <-deadline.C:
			if t.Check != nil {
				return t.Check()
			}
			return nil
		case <-tick.C:
			if t.Check != nil {
				if err := t.Check(); err != nil {
					return err
				}
			}
			if t.Wake == nil || t.Connected == nil || !t.Connected() {
				return nil
			}
		}
	}
}

// ChangeStream is a scoped, redacted subscription. The adapter owns its socket;
// closing the task cancels and joins the reconnect worker before returning.
type ChangeStream struct {
	Changes  <-chan state.StateChanged
	Seq      uint64
	Identity string
	Close    func()
	Dropped  func() bool
}
type ConnectChanges func(context.Context, uint64) (ChangeStream, error)

func OpenChanges(ctx context.Context, worktreeID string, retry time.Duration, connect ConnectChanges) (Trigger, func()) {
	if retry <= 0 {
		retry = time.Second
	}
	first, err := connect(ctx, 0)
	if err != nil {
		return Trigger{}, func() {}
	}
	task, cancel := context.WithCancel(ctx)
	wake := make(chan struct{}, 1)
	done := make(chan struct{})
	var connected atomic.Bool
	connected.Store(true)
	notify := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	go func() {
		defer close(done)
		defer close(wake)
		defer connected.Store(false)
		stream := first
		seq, identity := first.Seq, first.Identity
		for {
			connected.Store(true)
			for {
				select {
				case <-task.Done():
					if stream.Close != nil {
						stream.Close()
					}
					return
				case change, ok := <-stream.Changes:
					if !ok || (stream.Dropped != nil && stream.Dropped()) {
						goto reconnect
					}
					if change.Seq <= seq {
						continue
					}
					seq = change.Seq
					if change.Source != nil && change.Source.WorktreeID != worktreeID {
						continue
					}
					if RelevantChange(change.Changed) {
						notify()
					}
				}
			}
		reconnect:
			connected.Store(false)
			if stream.Close != nil {
				stream.Close()
			}
			for {
				timer := time.NewTimer(retry)
				select {
				case <-task.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				next, err := connect(task, seq)
				if err != nil {
					continue
				}
				if next.Identity != identity {
					seq = next.Seq
				} else if next.Seq > seq {
					seq = next.Seq
				}
				identity = next.Identity
				stream = next
				notify()
				break
			}
		}
	}()
	return Trigger{Wake: wake, Connected: connected.Load}, func() { cancel(); <-done }
}
func RelevantChange(changed []string) bool {
	for _, kind := range changed {
		switch kind {
		case "port_changed", "health_changed", "service_changed", "service_failed", "worktree_changed", "scan_error", "db_reset", "daemon_stopping", "port_occupied", "ready_timeout", "manifest_runtime_conflict", "repeated_crash":
			return true
		}
	}
	return false
}
