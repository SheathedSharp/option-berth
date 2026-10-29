package workplace

import (
	"sync/atomic"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/runsreg"
)

// Closing the block of a run the daemon outlived.
//
// `oberth start -d` hands a command to the daemon and returns. The CLI that
// asked for it is gone before the command ends, so the block it opened would
// stay `running` for good. The daemon is the process that owns the run and
// learns how it ended, so it is the one that closes the block — through the
// run's own id, which is the link between the two records.

// runtime is the daemon this process is serving, while it is serving one. The
// hook is registered once, at init, and needs a store only while a daemon is
// up: a run exit arriving with no daemon behind it has nothing to write to.
var runtime atomic.Pointer[daemon.Runtime]

func init() {
	daemon.OnStartChecked(func(rt *daemon.Runtime) error {
		runtime.Store(rt)
		return nil
	})
	daemon.OnShutdown(func(bool) { runtime.Store(nil) })
	daemon.OnRunExit(closeBlockForRun)
}

// closeBlockForRun closes the block that recorded the run which just ended.
//
// It runs on the reaper's path and must stay short. A failure is logged and
// dropped: a record that could not be closed is not a reason to disturb the
// exit path that every service stop depends on.
func closeBlockForRun(e daemon.RunExit) {
	rt := runtime.Load()
	if rt == nil || rt.Store == nil || e.RunID == "" {
		return
	}
	code := e.Code
	closed, err := rt.Store.Workplaces().CloseBlocksByRun(
		e.RunID, string(StatusForRunExit(e.Code, e.Reason)), &code, e.ExitedAt, TerminalStatuses())
	if err != nil {
		if rt.Logger != nil {
			rt.Logger.Warn("closing the workplace block for a finished run", "run", e.RunID, "error", err)
		}
		return
	}
	if closed > 0 && rt.Logger != nil {
		rt.Logger.Debug("closed a workplace block", "run", e.RunID, "reason", e.Reason)
	}
}

// StatusForRunExit maps how a run ended onto the status of the block that
// recorded it.
//
// A run option-berth or the user stopped is `interrupted` whatever its exit
// code: the signal is the outcome, and calling a deliberate stop a failure
// would be reading the number instead of the fact. Everything else follows the
// code — zero completed, anything else failed.
func StatusForRunExit(code int, reason string) Status {
	if reason == runsreg.ReasonStopped {
		return StatusInterrupted
	}
	if code == 0 {
		return StatusCompleted
	}
	return StatusFailed
}
