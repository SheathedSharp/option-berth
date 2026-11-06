package killer

import (
	"context"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// escalate is SIGTERM, then SIGKILL for whatever is still standing once the
// grace period is over. It polls instead of sleeping out the window, so a
// process that exits promptly costs one poll interval rather than five seconds.
//
// Cancellation is checked around every observation, not only at the sleeps: a
// cancelled request must not read a probe it never trusted as evidence that a
// listener closed, and it must never report a row it interrupted as a success.
func (e *engine) escalate(ctx context.Context, units []*unit, opts Options) {
	pending := escalateCandidates(units)
	cancelled := func() bool { return noteCancellation(ctx, pending) }
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
		e.hardKill(u)
	}
}

// escalateCandidates is the units whose initial signal actually landed. A unit
// that failed to resolve, targets a container, or has no accepted stop has
// nothing left to escalate.
func escalateCandidates(units []*unit) []*unit {
	candidates := make([]*unit, 0, len(units))
	for _, u := range units {
		if u.refusal != nil || u.container != "" {
			continue
		}
		if anyStopOK(u) {
			candidates = append(candidates, u)
		}
	}
	return candidates
}

// anyStopOK reports whether at least one of the unit's actions succeeded.
func anyStopOK(u *unit) bool {
	for _, s := range u.stops {
		if s.ok {
			return true
		}
	}
	return false
}

// noteCancellation records a cancelled stop on every action that still claims
// success, and reports whether the context was cancelled at all.
//
// A cancelled kill is not a completed one: the signal went out, so the action is
// not a plain failure either, and the message says which of the two happened.
func noteCancellation(ctx context.Context, pending []*unit) bool {
	err := ctx.Err()
	if err == nil {
		return false
	}
	interrupted := "stop interrupted after initial signal: " + err.Error()
	for _, u := range pending {
		for i := range u.stops {
			if !u.stops[i].ok {
				continue
			}
			u.stops[i].ok = false
			u.stops[i].err = codedf(Code(err), "", "%s", interrupted)
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
// children-before-parents order. Only an action that already succeeded is
// escalated: a signal the OS refused the first time is not retried as force,
// and a refused identity is re-checked before every escalation for the same
// reason it was checked before every first signal.
func (e *engine) hardKill(u *unit) {
	kill := func(s *stop, err error) {
		if err != nil {
			// No SIGKILL was sent; do not rewrite the method into a
			// fictitious action.
			s.ok = false
			s.err = err
			return
		}
		s.method = state.MethodSIGKILL
		s.ok = true
		s.err = nil
	}
	refuse := func(s *stop, err error) {
		s.ok = false
		s.err = err
	}

	if u.pgid != 0 {
		s := &u.stops[0]
		if !s.ok {
			return
		}
		if err := e.guard(u, u.pgid); err != nil {
			refuse(s, err)
			return
		}
		kill(s, e.signalResult(u, e.signalGrp(u.pgid, true)))
		return
	}
	for i, pid := range u.pids {
		s := &u.stops[i]
		if !s.ok {
			continue
		}
		if err := e.ownerError(); err != nil {
			refuse(s, err)
			continue
		}
		if !e.alive(pid) {
			continue
		}
		if err := e.guard(u, pid); err != nil {
			refuse(s, err)
			continue
		}
		if e.nativeTree && u.tree {
			kill(s, e.signalResult(u, e.signalTree(pid, true)))
		} else {
			kill(s, e.signalResult(u, e.signalProc(pid, true)))
		}
	}
}
