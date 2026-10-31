package groupstart

import (
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

// The phases a start attempt reports while it has not produced an outcome.
const (
	phaseWaitingDependency = "waiting_dependency"
	phasePrepare           = "prepare"
	phaseSpawning          = "spawning"
)

func init() {
	daemon.RegisterStartAttemptLookup(lookupStartAttempt)
}

// startAttempt is one service's in-flight start, owned by this package. It is
// memory-only diagnostic state keyed by daemon runtime + group + service: it
// never enters the run registry, never reaches the store, and is removed on
// every exit path (already-running isn't attempted at all; a dependency or
// prepare failure, a spawn failure, cancellation and success all call finish).
// Once the run is registered the run record is the only owner; a daemon restart
// simply loses every attempt.
type startAttempt struct {
	rt            *daemon.Runtime
	group         string
	service       string
	startID       string
	startedAt     time.Time
	readyTimeout  time.Duration
	phase         string
	waitingOn     []string
	lastPrepareMS *int64
}

type startAttemptKey struct {
	rt      *daemon.Runtime
	group   string
	service string
}

var startAttempts struct {
	sync.Mutex
	byKey map[startAttemptKey]*startAttempt
}

func (a *startAttempt) key() startAttemptKey {
	return startAttemptKey{rt: a.rt, group: a.group, service: a.service}
}

// beginStartAttempt records the attempt and returns its handle. waitingOn names
// the dependencies this service is about to wait for (empty when it has none).
func beginStartAttempt(rt *daemon.Runtime, group, startID string, svc groups.Service, waitingOn []string, lastPrepareMS *int64) *startAttempt {
	a := &startAttempt{
		rt:            rt,
		group:         group,
		service:       svc.Name,
		startID:       startID,
		startedAt:     time.Now(),
		readyTimeout:  svc.EffectiveReadyTimeout(),
		phase:         phaseWaitingDependency,
		waitingOn:     append([]string{}, waitingOn...),
		lastPrepareMS: copyInt64Ptr(lastPrepareMS),
	}
	startAttempts.Lock()
	defer startAttempts.Unlock()
	if startAttempts.byKey == nil {
		startAttempts.byKey = map[startAttemptKey]*startAttempt{}
	}
	startAttempts.byKey[a.key()] = a
	return a
}

// setPhase moves the attempt to a new phase and replaces what it is waiting on.
func (a *startAttempt) setPhase(phase string, waitingOn []string) {
	startAttempts.Lock()
	defer startAttempts.Unlock()
	if startAttempts.byKey[a.key()] != a {
		return
	}
	a.phase = phase
	a.waitingOn = append([]string{}, waitingOn...)
}

// setWaitingOn replaces what the attempt is waiting on without touching its
// phase. The dependency poll calls it so a reader watching a start sees the set
// shrink as dependencies come up, instead of the declaration it began with.
//
// It only assigns under the lock; the observation that produced the names is
// the caller's, and no pendingContext read happens with this lock held. An
// attempt already finished or replaced by a newer one is left alone, so a poll
// that lands after the attempt ended cannot resurrect stale waiting facts.
func (a *startAttempt) setWaitingOn(names []string) {
	startAttempts.Lock()
	defer startAttempts.Unlock()
	if startAttempts.byKey[a.key()] != a {
		return
	}
	a.waitingOn = append([]string{}, names...)
}

// setLastPrepare records the duration of the prepare step that just ran. It is
// advisory: a reader may show it, but no start decision reads it.
func (a *startAttempt) setLastPrepare(ms int64) {
	startAttempts.Lock()
	defer startAttempts.Unlock()
	if startAttempts.byKey[a.key()] != a {
		return
	}
	a.lastPrepareMS = &ms
}

// finish removes the attempt. It only deletes its own entry, so a stale handle
// can never erase a newer attempt for the same service.
func (a *startAttempt) finish() {
	startAttempts.Lock()
	defer startAttempts.Unlock()
	if startAttempts.byKey[a.key()] == a {
		delete(startAttempts.byKey, a.key())
	}
}

// progressChunk renders the attempt as a `starting` chunk. elapsed_ms is
// computed now, so a reader sees how long this attempt has been in flight.
func (a *startAttempt) progressChunk() rpc.GroupsStartChunk {
	startAttempts.Lock()
	defer startAttempts.Unlock()
	return startAttemptChunk(a)
}

// startAttemptChunk builds the wire chunk while the caller holds the lock.
func startAttemptChunk(a *startAttempt) rpc.GroupsStartChunk {
	elapsed := time.Since(a.startedAt).Milliseconds()
	if elapsed < 0 {
		elapsed = 0
	}
	waiting := make([]string, 0, len(a.waitingOn))
	waiting = append(waiting, a.waitingOn...)
	return rpc.GroupsStartChunk{
		Service:       a.service,
		State:         "starting",
		Phase:         a.phase,
		StartedAt:     a.startedAt.UTC().Format(time.RFC3339Nano),
		ReadyTimeout:  a.readyTimeout.String(),
		ElapsedMS:     elapsed,
		WaitingOn:     waiting,
		LastPrepareMS: copyInt64Ptr(a.lastPrepareMS),
	}
}

// lookupStartAttempt answers the daemon's snapshot join. It copies the attempt
// so the caller cannot race the start call's updates.
func lookupStartAttempt(rt *daemon.Runtime, group, service string) (daemon.StartAttempt, bool) {
	startAttempts.Lock()
	defer startAttempts.Unlock()
	a := startAttempts.byKey[startAttemptKey{rt: rt, group: group, service: service}]
	if a == nil {
		return daemon.StartAttempt{}, false
	}
	return daemon.StartAttempt{
		Phase:         a.phase,
		StartedAt:     a.startedAt,
		ReadyTimeout:  a.readyTimeout,
		WaitingOn:     append([]string{}, a.waitingOn...),
		LastPrepareMS: copyInt64Ptr(a.lastPrepareMS),
	}, true
}

// sendStartProgress pushes one in-flight chunk when the caller asked for
// progress. It is a no-op otherwise, which is what keeps old clients on the old
// stream.
func sendStartProgress(s *daemon.Stream, p rpc.GroupsStartParams, a *startAttempt) error {
	if !p.Progress {
		return nil
	}
	return s.Send(a.progressChunk())
}

func copyInt64Ptr(v *int64) *int64 {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
