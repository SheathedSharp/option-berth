package killer

import (
	"context"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// escalate is SIGTERM, then SIGKILL for whatever is still standing once the
// grace period is over. It polls instead of sleeping out the window, so a
// process that exits promptly costs one poll interval rather than five seconds.
// A Docker unit and a unit that never resolved are not escalated: the first has
// no signal to escalate to, the second never ran.
func (e *engine) escalate(ctx context.Context, units []*unit, opts Options, results []Result) {
	pending := escalateCandidates(units, results)
	cancelled := func() bool { return noteCancellation(ctx, pending, results) }
	scratch := make([]*unit, 0, len(pending))
	deadline := e.clock.Now().Add(opts.grace())

	for {
		if cancelled() {
			return
		}
		remaining := e.stillUp(pending, scratch)
		// A cancelled probe is not evidence that a listener closed, so the
		// cancellation check comes before pending is replaced.
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

// escalateCandidates is the units whose initial signal actually landed. A unit
// that failed to resolve, targets a container, or whose rows are all already
// failures has nothing left to escalate.
func escalateCandidates(units []*unit, results []Result) []*unit {
	candidates := make([]*unit, 0, len(units))
	for _, u := range units {
		if u.err != nil || u.container != "" {
			continue
		}
		if anyRowOK(u, results) {
			candidates = append(candidates, u)
		}
	}
	return candidates
}

// anyRowOK reports whether at least one of the unit's rows records a success.
func anyRowOK(u *unit, results []Result) bool {
	for _, row := range u.rows {
		if results[row].OK {
			return true
		}
	}
	return false
}

// noteCancellation records a cancelled stop on every row that still claims
// success, and reports whether the context was cancelled at all.
//
// A cancelled kill is not a completed one: the signal went out, so the row is
// not a plain failure either, and the message says which of the two happened.
func noteCancellation(ctx context.Context, pending []*unit, results []Result) bool {
	err := ctx.Err()
	if err == nil {
		return false
	}
	for _, u := range pending {
		for _, row := range u.rows {
			if !results[row].OK {
				continue
			}
			results[row].OK = false
			results[row].Error = "stop interrupted after initial signal: " + err.Error()
			results[row].Code = Code(err)
		}
	}
	return true
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
