package daemon

import (
	"context"
	"sync"
)

// groupLock exists only while an operation holds or waits for its token.
// users is protected by Runtime.groupLocksMu, not by the per-group token.
type groupLock struct {
	held  chan struct{}
	users int
}

// AcquireGroup serializes a group's lifecycle and returns its one-shot release.
// Admission references are registered before waiting and released on every
// failure path. Unrelated groups never share a token. Idle group names are
// removed, so churn does not retain one lock for every worktree ever observed.
func (r *Runtime) AcquireGroup(ctx context.Context, group string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.groupLocksMu.Lock()
	if r.groupLocks == nil {
		r.groupLocks = make(map[string]*groupLock)
	}
	lock := r.groupLocks[group]
	if lock == nil {
		lock = &groupLock{held: make(chan struct{}, 1)}
		r.groupLocks[group] = lock
	}
	lock.users++
	r.groupLocksMu.Unlock()

	drop := func() {
		r.groupLocksMu.Lock()
		lock.users--
		if lock.users == 0 {
			delete(r.groupLocks, group)
		}
		r.groupLocksMu.Unlock()
	}
	select {
	case lock.held <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-lock.held
			drop()
			return nil, err
		}
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-lock.held
			drop()
		})
	}, nil
}
