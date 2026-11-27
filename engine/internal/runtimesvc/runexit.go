package runtimesvc

import (
	"sync"
	"time"
)

// OnRunExit tells interested packages that a run ended.
//
// It exists because a run's end is observed in one place — the run registry,
// where the process exit, the exit code and the reason are already decided —
// while what that end *means* to a record belongs to whoever recorded the run.
// The workplace record uses it to close the block a detached run opened: the
// CLI that asked for the run is long gone by then, and the runtime service is the only
// thing left that can say how it finished.
//
// A callback must not block: it runs on the reaper's path, after the exit has
// been recorded. It must not assume it is the only one, and it must cope with
// being told about a run it never recorded — a run started by another build,
// or one whose block was already deleted.
type RunExit struct {
	// RunID is the run that ended, as `runs.register` and `oberth start`
	// report it. It is empty for a run registered without an id.
	RunID string
	// PID is the process that ran, for a run whose id is not enough to find.
	PID int
	// Code is the exit status; Reason says what it means (runsreg's Reason*
	// constants). A run that never started has a code of its own making.
	Code   int
	Reason string
	// ExitedAt is when the runtime service recorded the end.
	ExitedAt time.Time
}

type runExitHook struct {
	fn func(RunExit)
}

var (
	runExitMu    sync.Mutex
	runExitHooks []*runExitHook
)

// OnRunExit registers a callback run for every run whose end this runtime
// service records. It lives for the process lifetime; a component with a
// shorter life registers with RegisterRunExit so it can detach.
func OnRunExit(f func(RunExit)) {
	RegisterRunExit(f)
}

// RegisterRunExit registers a callback and returns the function that removes
// it. The cancellation makes the hook safe for a Server: a test that builds
// and stops many servers must not leave every one of them listening to the
// next exit.
func RegisterRunExit(f func(RunExit)) (cancel func()) {
	if f == nil {
		return func() {}
	}
	hook := &runExitHook{fn: f}
	runExitMu.Lock()
	runExitHooks = append(runExitHooks, hook)
	runExitMu.Unlock()
	return func() {
		runExitMu.Lock()
		defer runExitMu.Unlock()
		for i, existing := range runExitHooks {
			if existing == hook {
				runExitHooks = append(runExitHooks[:i], runExitHooks[i+1:]...)
				return
			}
		}
	}
}

// NotifyRunExit runs the registered callbacks. The run registry calls it; a
// package that only wants to hear about exits registers with OnRunExit.
func NotifyRunExit(e RunExit) {
	runExitMu.Lock()
	hooks := make([]func(RunExit), 0, len(runExitHooks))
	for _, hook := range runExitHooks {
		hooks = append(hooks, hook.fn)
	}
	runExitMu.Unlock()
	for _, f := range hooks {
		f(e)
	}
}
