package daemon

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
// CLI that asked for the run is long gone by then, and the daemon is the only
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
	// ExitedAt is when the daemon recorded the end.
	ExitedAt time.Time
}

var (
	runExitMu    sync.Mutex
	runExitHooks []func(RunExit)
)

// OnRunExit registers a callback run for every run whose end this daemon
// records.
func OnRunExit(f func(RunExit)) {
	if f == nil {
		return
	}
	runExitMu.Lock()
	defer runExitMu.Unlock()
	runExitHooks = append(runExitHooks, f)
}

// NotifyRunExit runs the registered callbacks. The run registry calls it; a
// package that only wants to hear about exits registers with OnRunExit.
func NotifyRunExit(e RunExit) {
	runExitMu.Lock()
	hooks := append([]func(RunExit){}, runExitHooks...)
	runExitMu.Unlock()
	for _, f := range hooks {
		f(e)
	}
}
