package runsreg

import (
	"context"
	"golang.org/x/sync/semaphore"
)

// Weight one preserves the existing mirrorMu exclusion and lock order, while
// allowing reservation finalization to queue with cancellation. The official
// semaphore owns waiter removal/handoff; no helper goroutine or polling lock.
// Like Registry, construct through New and never copy after use.
type mirrorMutex struct{ gate *semaphore.Weighted }

func (m *mirrorMutex) Lock()                                 { _ = m.gate.Acquire(context.Background(), 1) }
func (m *mirrorMutex) Unlock()                               { m.gate.Release(1) }
func (m *mirrorMutex) TryLock() bool                         { return m.gate.TryAcquire(1) }
func (m *mirrorMutex) LockContext(ctx context.Context) error { return m.gate.Acquire(ctx, 1) }
