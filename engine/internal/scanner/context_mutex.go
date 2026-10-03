package scanner

import (
	"context"
	"sync"
)

// contextMutex reuses the scanner's admission gate for locks that can cover
// external work. Its zero value is usable. It must not be copied after use.
// There is no waiter goroutine: cancellation leaves the owner's token alone.
// A channel is allocated once per lock, not once per acquisition.
type contextMutex struct {
	once sync.Once
	gate chan struct{}
}

func (m *contextMutex) init() {
	m.once.Do(func() { m.gate = make(chan struct{}, 1) })
}

// Lock retains the synchronous contract for setters and compatibility callers.
func (m *contextMutex) Lock() {
	m.init()
	lock(m.gate)
}

func (m *contextMutex) LockContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.init()
	return lockContext(ctx, m.gate)
}

func (m *contextMutex) Unlock() { unlock(m.gate) }
