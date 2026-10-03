package runsreg

import (
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon"
)

var pruning struct {
	sync.Mutex
	stop chan struct{}
	done chan struct{}
}

// startPruning owns one worker, as before. Replacement and shutdown join the
// old worker before closing its store or starting another owner.
func startPruning(rt *daemon.Runtime) {
	pruning.Lock()
	defer pruning.Unlock()
	stopPruningLocked()
	stop, done := make(chan struct{}), make(chan struct{})
	pruning.stop, pruning.done = stop, done
	go func() {
		ticker := time.NewTicker(pruneInterval)
		defer close(done)
		defer ticker.Stop()
		runPruning(stop, ticker.C, func() { rt.Runs().Prune() })
	}()
}

func runPruning(stop <-chan struct{}, ticks <-chan time.Time, prune func()) {
	for {
		select {
		case <-stop:
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			select {
			case <-stop:
				return
			default:
			}
			prune()
		}
	}
}

func stopPruning() {
	pruning.Lock()
	defer pruning.Unlock()
	stopPruningLocked()
}

func stopPruningLocked() {
	if pruning.stop == nil {
		return
	}
	close(pruning.stop)
	<-pruning.done // in-flight synchronous pruning must finish, never detach
	pruning.stop, pruning.done = nil, nil
}
