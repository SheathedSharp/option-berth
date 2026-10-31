package daemon

import (
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// StartAttempt is one in-flight `groups.start` service attempt as the daemon
// can describe it. It is memory-only, owned and cleaned up by the start call
// (internal/daemon/groupstart): it is not a run, is never written to the store,
// never counts as running, and disappears with the daemon. The daemon package
// only reads it to join a reader-visible `starting` object onto a snapshot.
type StartAttempt struct {
	Phase         string
	StartedAt     time.Time
	ReadyTimeout  time.Duration
	WaitingOn     []string
	LastPrepareMS *int64
}

// StartAttemptLookup answers with the current attempt for one group+service.
// The owning package installs it so this package never imports groupstart
// (contract §8: the daemon must not depend on the package that registers a
// handler).
type StartAttemptLookup func(rt *Runtime, group, service string) (StartAttempt, bool)

var startAttemptLookup struct {
	sync.RWMutex
	fn StartAttemptLookup
}

// RegisterStartAttemptLookup installs the attempt source. Called once from the
// owning package's init().
func RegisterStartAttemptLookup(fn StartAttemptLookup) {
	startAttemptLookup.Lock()
	defer startAttemptLookup.Unlock()
	startAttemptLookup.fn = fn
}

func lookupStartAttempt(rt *Runtime, group, service string) (StartAttempt, bool) {
	startAttemptLookup.RLock()
	fn := startAttemptLookup.fn
	startAttemptLookup.RUnlock()
	if fn == nil {
		return StartAttempt{}, false
	}
	return fn(rt, group, service)
}

// joinStartAttempts returns a copy of groups with each service's in-flight
// attempt attached. The input is never modified: the scanner's cached snapshot
// must stay attempt-free so a delta cannot publish it and a later reader cannot
// mistake it for a run. Only the groups and service slices that actually carry
// an attempt are copied.
func joinStartAttempts(rt *Runtime, groups []state.Group) []state.Group {
	if len(groups) == 0 {
		return groups
	}
	out := make([]state.Group, len(groups))
	for i := range groups {
		out[i] = groups[i]
		var services []state.Service
		for j := range groups[i].Services {
			attempt, ok := lookupStartAttempt(rt, groups[i].Name, groups[i].Services[j].Name)
			if !ok {
				continue
			}
			if services == nil {
				services = append([]state.Service(nil), groups[i].Services...)
			}
			services[j].Starting = startingFromAttempt(attempt)
		}
		if services != nil {
			out[i].Services = services
		}
	}
	return out
}

// startingFromAttempt projects an attempt at read time. elapsed_ms is computed
// here rather than stored, so a progress read shows how long the attempt has
// actually been in flight.
func startingFromAttempt(a StartAttempt) *state.ServiceStarting {
	elapsed := time.Since(a.StartedAt).Milliseconds()
	if elapsed < 0 {
		elapsed = 0
	}
	waiting := make([]string, 0, len(a.WaitingOn))
	waiting = append(waiting, a.WaitingOn...)
	return &state.ServiceStarting{
		Phase:         a.Phase,
		StartedAt:     a.StartedAt.UTC().Format(time.RFC3339Nano),
		ReadyTimeout:  a.ReadyTimeout.String(),
		ElapsedMS:     elapsed,
		WaitingOn:     waiting,
		LastPrepareMS: copyInt64(a.LastPrepareMS),
	}
}

func copyInt64(v *int64) *int64 {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
