package killer

import (
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
)

func (u *unit) addClaim(pid int, at time.Time) {
	if pid <= 0 || at.IsZero() {
		return
	}
	if u.claims == nil {
		u.claims = make(map[int]time.Time)
	}
	// Keep the older constraint; matching a listener cannot weaken a run claim.
	if old, ok := u.claims[pid]; !ok || at.Before(old) {
		u.claims[pid] = at
	}
}

func (e *engine) ownerError() error {
	if e.ctx != nil {
		return e.ctx.Err()
	}
	return nil
}

func (e *engine) captureIdentity(u *unit) {
	if u.err != nil {
		return
	}
	if err := e.ownerError(); err != nil {
		u.err = err
		return
	}
	if u.container != "" || e.startTime == nil {
		return
	}
	u.observed = make(map[int]time.Time, len(u.pids)+2)
	seen := make(map[int]bool, len(u.pids)+2)
	capture := func(pid int) {
		if pid <= 0 || seen[pid] || u.err != nil {
			return
		}
		seen[pid] = true
		if err := e.ownerError(); err != nil {
			u.err = err
			return
		}
		at, ok := e.startTime(pid)
		if err := e.ownerError(); err != nil {
			u.err = err
			return
		}
		if ok && !at.IsZero() {
			if claimed := u.claims[pid]; !claimed.IsZero() && at.After(claimed.Add(ports.StartTolerance)) {
				u.err = replacedPID(pid)
				return
			}
			u.observed[pid] = at
		}
	}
	capture(u.root)
	capture(u.listenPID)
	for _, pid := range u.pids {
		capture(pid)
	}
}

func replacedPID(pid int) error {
	return codedf(CodeNotFound, "refresh the target before trying again",
		"pid %d now belongs to a different process; not signalling it", pid)
}

// guard re-observes birth evidence at EVERY signal, including SIGKILL. The
// production reader bypasses observation caches. Unknown never proves identity;
// previously-known evidence disappearing refuses the signal. The historical
// no-evidence fallback remains, without claiming a portable atomic PID handle.
func (e *engine) guard(u *unit, pid int) error {
	if err := e.ownerError(); err != nil {
		return err
	}
	if u.container != "" {
		return nil
	}
	check := func(p int) error {
		if p <= 0 || e.startTime == nil {
			return nil
		}
		at, ok := e.startTime(p)
		if err := e.ownerError(); err != nil {
			return err
		}
		before, known := u.observed[p]
		if known && (!ok || at.IsZero()) {
			return codedf(CodePermissionDenied, "refresh process identity",
				"cannot revalidate the recorded identity of PID %d; not signalling it", p)
		}
		if !ok || at.IsZero() {
			return nil
		}
		if known && !at.Equal(before) {
			return replacedPID(p)
		}
		if claimed := u.claims[p]; !claimed.IsZero() && at.After(claimed.Add(ports.StartTolerance)) {
			return replacedPID(p)
		}
		// Once any signal boundary obtained positive evidence, later phases
		// must not fall back to the plan's earlier unknown state.
		if !known {
			if u.observed == nil {
				u.observed = make(map[int]time.Time)
			}
			u.observed[p] = at
		}
		return nil
	}
	// Recheck the owner and the actual target. For group signalling also
	// check the listener; for a manual tree walk do not require an unrelated
	// child, already stopped by us, to remain observable.
	if err := check(u.root); err != nil {
		return err
	}
	if u.pgid != 0 && u.listenPID != u.root {
		if err := check(u.listenPID); err != nil {
			return err
		}
	}
	if pid != u.root && (u.pgid == 0 || pid != u.listenPID) {
		if err := check(pid); err != nil {
			return err
		}
	}
	if u.pgid != 0 {
		group, ok := e.groupOf(u.root)
		if !ok || group != u.pgid {
			return codedf(CodeNotFound, "", "process group ownership changed; not signalling it")
		}
	}
	return e.ownerError()
}
