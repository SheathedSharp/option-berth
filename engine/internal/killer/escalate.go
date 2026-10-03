package killer

import (
	"context"

	"github.com/sheathedsharp/option-berth/internal/state"
)

// escalate implements the daemon spec's "SIGTERM, wait up to --grace (default
// 5s), then SIGKILL if the port is still open". It polls the targets rather
// than sleeping out the whole grace period, so a well-behaved process costs
// one poll interval instead of five seconds. Docker units and units that
// failed to resolve are never escalated.
func (e *engine) escalate(ctx context.Context, units []*unit, opts Options, results []Result) {
	pending := make([]*unit, 0, len(units))
	for _, u := range units {
		if u.err != nil || u.container != "" {
			continue
		}
		for _, row := range u.rows {
			if results[row].OK {
				pending = append(pending, u)
				break
			}
		}
	}
	cancelled := func() bool {
		if ctx.Err() == nil {
			return false
		}
		for _, u := range pending {
			for _, row := range u.rows {
				if results[row].OK {
					results[row].OK = false
					results[row].Error = "stop interrupted after initial signal: " + ctx.Err().Error()
					results[row].Code = Code(ctx.Err())
				}
			}
		}
		return true
	}
	scratch := make([]*unit, 0, len(pending))
	deadline := e.clock.Now().Add(opts.grace())
	for {
		if cancelled() {
			return
		}
		remaining := e.stillUp(pending, scratch)
		// A cancelled TCP probe is not proof that a listener closed. Preserve the
		// original pending list until after the ownership check.
		if cancelled() {
			return
		}
		scratch, pending = pending[:0], remaining
		if len(pending) == 0 {
			return
		}
		if !e.clock.Now().Before(deadline) {
			break
		}
		if !e.pause(ctx, pollInterval) {
			cancelled()
			return
		}
	}
	for _, u := range pending {
		if cancelled() {
			return
		}
		e.hardKill(u, results)
	}
}

// stillUp keeps the units that have not gone away. A unit anchored on a port is
// judged by the port: that is the user-visible promise of `oberth kill 3000`. A
// unit addressed by pid alone is judged by its processes.
//
// Only a refusal drops a port unit. A probe that timed out has shown nothing —
// the listener is still sitting on the port, it just is not answering — and
// treating that as "gone" ends the escalation early and reports a kill that did
// not happen. The cost of the other error is one SIGKILL to a process that
// already died, which is nothing.
func (e *engine) stillUp(units, scratch []*unit) []*unit {
	out := scratch[:0]
	for _, u := range units {
		if u.port > 0 {
			if e.probe(u.port, u.bind) != portClosed {
				out = append(out, u)
			}
			continue
		}
		if e.anyAlive(u) {
			out = append(out, u)
		}
	}
	return out
}

// anyAlive reports whether any process of the unit is still running.
func (e *engine) anyAlive(u *unit) bool {
	if u.pgid != 0 {
		return e.alive(u.pgid)
	}
	for _, pid := range u.pids {
		if e.alive(pid) {
			return true
		}
	}
	return false
}

// hardKill sends SIGKILL to whatever of the unit is left, keeping the
// children-before-parents order, and rewrites the affected rows to sigkill.
func (e *engine) hardKill(u *unit, results []Result) {
	reject := func(row int, err error) {
		// No SIGKILL was sent; do not rewrite the method into a fictitious action.
		results[row].OK = false
		results[row].Error = err.Error()
		results[row].Code = Code(err)
	}
	mark := func(row int, err error) {
		results[row].Method = state.MethodSIGKILL
		results[row].OK = err == nil
		results[row].Error = ""
		results[row].Code = Code(err)
		if err != nil {
			results[row].Error = err.Error()
		}
	}
	if u.pgid != 0 {
		if len(u.rows) == 0 || !results[u.rows[0]].OK {
			return
		}
		if err := e.guard(u, u.pgid); err != nil {
			reject(u.rows[0], err)
			return
		}
		mark(u.rows[0], e.signalResult(u, e.signalGrp(u.pgid, true)))
		return
	}
	for i, pid := range u.pids {
		if i >= len(u.rows) || !results[u.rows[i]].OK {
			continue
		}
		if err := e.ownerError(); err != nil {
			reject(u.rows[i], err)
			continue
		}
		if !e.alive(pid) {
			continue
		}
		if err := e.guard(u, pid); err != nil {
			reject(u.rows[i], err)
			continue
		}
		if e.nativeTree && u.tree {
			mark(u.rows[i], e.signalResult(u, e.signalTree(pid, true)))
		} else {
			mark(u.rows[i], e.signalResult(u, e.signalProc(pid, true)))
		}
	}
}
