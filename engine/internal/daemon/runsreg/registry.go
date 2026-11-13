// Package runsreg is the daemon's registry of processes started through
// `oberth start` (and `runs.spawn`). It answers three questions: what is
// running, which run owns a listening port, and where does a detached run log.
//
// The registry is in memory and authoritative while the daemon lives. Live
// runs also mirror themselves into ~/.option-berth/runs.json for the direct
// reader and restart recovery; the daemon attributes from this registry.
// Finished runs are persisted in the daemon store so their evidence survives
// a daemon restart.
package runsreg

import (
	"log/slog"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/sheathedsharp/option-berth/engine/internal/codeid"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runs"
	"github.com/sheathedsharp/option-berth/engine/internal/sessions"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// parentsTTL bounds how long one process table is reused while attributing a
// snapshot's worth of ports.
const parentsTTL = 2 * time.Second

// The PPID-walk bounds and cycle guards live with the process table, in
// ports.Ancestor; this registry only supplies the lookup.

// Record is one registered run: which process option-berth started, what it was
// started as, and where its output goes.
type Record struct {
	// ID identifies the run even after its pid is recycled; PID and PPID are the
	// process and its parent at the moment it was registered.
	ID   string
	PID  int
	PPID int
	// Group and Name are the manifest identity the run was started under; Cmd
	// and Cwd are what it was started with.
	Group string
	Name  string
	Cmd   string
	Cwd   string
	// PortHint is the port the caller expected this run to bind, 0 for a run
	// that declares none.
	PortHint int
	// StartedAt is when the process started, in the runner's own reading.
	StartedAt time.Time
	// ConfigPath, StartID and Origin say where a run came from: the
	// oberth.yaml it was started from, the groups.start that started it with
	// its siblings, and the client name daemon.hello reported for it.
	ConfigPath string
	StartID    string
	Origin     string
	SpecHash   string
	// LogPath is the file a detached run writes to and LogOffset is where this
	// run's part of it begins; the last lines are kept with the exit.
	LogPath   string
	LogOffset int64
	// Session is the agent session that asked for this run, or the zero value
	// when nothing did. It travels with the run so every port the
	// run opens can be stamped with it.
	Session state.Session

	// stopping is set when option-berth is about to stop this run, so its exit is
	// recorded as stopped rather than a crash.
	stopping bool
	// stoppingReason carries a more specific lifecycle outcome, such as a
	// readiness timeout, through the asynchronous reap.
	stoppingReason string
}

// Registry holds the live runs. The zero value is not usable; call New.
type Registry struct {
	mu   sync.Mutex
	runs map[int]Record

	// mirrorMu orders each mirrored mutation through its disk effect. Reads
	// use mu only. Lock order is mirrorMu -> mu; never hold mu during I/O.
	mirrorMu mirrorMutex

	// Alive reports whether a pid is still running. Tests replace it.
	Alive func(pid int) bool
	// Parents returns a pid -> ppid table for the ancestry walk. Tests replace
	// it; production reads the same process table the scanner builds.
	// A returned map is immutable after return, as with the production adapter.
	Parents func() map[int]int
	// StartTime reports when the process behind a pid started, and whether the
	// scan's table has an answer. It is the identity check that tells a
	// recycled pid from the process a record names. Tests replace it.
	StartTime func(pid int) (time.Time, bool)
	// Mirror writes every change through to runs.json. Off in tests.
	Mirror bool

	parents   map[int]int
	parentsAt time.Time
	// Admission tickets order refreshes even when their clocks are equal.
	parentsSeq       uint64
	parentsCommitted uint64
	now              func() time.Time

	// exits is the history of runs that ended, oldest first, capped at
	// maxExits. The daemon store is the durable copy; this slice is the hot
	// view used by RPC and group resolution.
	exits   []Exit
	history *store.Store
	logger  *slog.Logger // optional; defaults to the process logger
}

// New returns an empty registry that mirrors to runs.json.
func New() *Registry {
	return &Registry{
		mirrorMu:  mirrorMutex{gate: semaphore.NewWeighted(1)},
		runs:      map[int]Record{},
		Alive:     runs.PIDAlive,
		Parents:   ports.ParentTable,
		StartTime: ports.ProcessStart,
		Mirror:    true,
		now:       time.Now,
	}
}

// SetHistoryStore attaches the daemon store used for finished-run evidence.
// The registry remains usable without a store (for example in a read-only
// fallback or a unit test), but a daemon with a store calls this before it
// accepts clients.
func (r *Registry) SetHistoryStore(st *store.Store) {
	r.mu.Lock()
	r.history = st
	r.mu.Unlock()
}

// LoadExits replaces the in-memory exit view with the newest durable records.
// The store returns newest first while the registry keeps its internal slice
// oldest first, matching the existing cap and append behavior.
func (r *Registry) LoadExits(st *store.Store) error {
	if st == nil {
		r.mu.Lock()
		r.history = nil
		r.exits = nil
		r.mu.Unlock()
		return nil
	}
	rows, err := st.RunExits(maxExits)
	if err != nil {
		return err
	}
	exits := make([]Exit, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		exits = append(exits, exitFromStore(rows[i]))
	}
	r.mu.Lock()
	r.history = st
	r.exits = exits
	r.mu.Unlock()
	return nil
}

// Register records a run and returns it with its id filled in. Registering a
// pid twice replaces the entry: a re-registered pid is the same process.
func (r *Registry) Register(rec Record) Record {
	r.mirrorMu.Lock()
	defer r.mirrorMu.Unlock()
	if rec.StartedAt.IsZero() {
		rec.StartedAt = r.clock()
	}
	r.mu.Lock()
	rec = r.keepIDLocked(rec)
	r.runs[rec.PID] = rec
	r.mu.Unlock()

	r.mirrorAddLocked(rec)
	return rec
}

// keepIDLocked gives a re-registered pid the id it already had: registering the
// same pid twice replaces the entry, and the same process keeps its identity.
// The caller holds mu, so the id cannot be inherited from a run that is being
// dropped in between.
func (r *Registry) keepIDLocked(rec Record) Record {
	if existing, ok := r.runs[rec.PID]; ok && rec.ID == "" {
		rec.ID = existing.ID
	}
	return rec
}

// Unregister drops the run with this pid, reporting whether there was one.
func (r *Registry) Unregister(pid int) bool {
	r.mirrorMu.Lock()
	defer r.mirrorMu.Unlock()
	r.mu.Lock()
	rec, ok := r.runs[pid]
	delete(r.runs, pid)
	r.mu.Unlock()
	if ok {
		r.mirrorRemoveLocked(rec)
		_ = codeid.Remove(rec.ID)
	}
	return ok
}

// PruneCodeSnapshots removes source snapshots whose run is no longer live.
// A daemon crash can leave a snapshot behind; startup is when the live set is
// known and no new run has been started yet.
func (r *Registry) PruneCodeSnapshots() {
	r.mu.Lock()
	keep := make(map[string]bool, len(r.runs))
	for _, rec := range r.runs {
		if rec.ID != "" {
			keep[rec.ID] = true
		}
	}
	logger := r.logger
	r.mu.Unlock()
	if err := codeid.PruneExcept(keep); err != nil && logger != nil {
		logger.Warn("pruning code snapshots", "error", err)
	}
}

// RenameGroups follows a `groups.rename`: every run recorded under an old group
// name moves to the new one, so a service started before its project was
// renamed stays in the project's group instead of keeping a group of the old
// name to itself. It reports how many runs moved.
func (r *Registry) RenameGroups(renames map[string]string) int {
	if len(renames) == 0 {
		return 0
	}
	r.mirrorMu.Lock()
	defer r.mirrorMu.Unlock()
	r.mu.Lock()
	moved := r.renameRunsLocked(renames)
	r.renameExitsLocked(renames)
	history := r.history
	r.mu.Unlock()
	for _, rec := range moved {
		r.mirrorAddLocked(rec)
	}
	if history != nil {
		r.persistenceResult("exit.rename", history.RenameRunExitGroups(renames))
	}
	return len(moved)
}

// renameRunsLocked moves the live runs and answers with the ones it moved, so
// the caller can mirror each of them after mu is released. The caller holds mu.
func (r *Registry) renameRunsLocked(renames map[string]string) []Record {
	var moved []Record
	for pid, rec := range r.runs {
		next := renames[rec.Group]
		if next == "" {
			continue
		}
		rec.Group = next
		r.runs[pid] = rec
		moved = append(moved, rec)
	}
	return moved
}

// renameExitsLocked carries the recorded exits along with the runs: a run's
// history belongs to the group it was started in, under whichever name. The
// caller holds mu.
func (r *Registry) renameExitsLocked(renames map[string]string) {
	for i := range r.exits {
		if next := renames[r.exits[i].Group]; next != "" {
			r.exits[i].Group = next
		}
	}
}

// List returns the live runs, oldest first, after pruning the dead ones.
func (r *Registry) List() []Record {
	r.Prune()
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.visibleLocked()
	sort.Slice(out, func(i, j int) bool { return out[i].before(out[j]) })
	return out
}

// visibleLocked is what a reader may see: every registered run except the ones
// already being stopped, whose exit is about to be recorded. The caller holds
// mu.
func (r *Registry) visibleLocked() []Record {
	out := make([]Record, 0, len(r.runs))
	for _, rec := range r.runs {
		if !rec.stopping {
			out = append(out, rec)
		}
	}
	return out
}

// before is the order List publishes: oldest start first, with the pid breaking
// a tie so two runs started in the same instant keep a stable order.
func (rec Record) before(other Record) bool {
	if !rec.StartedAt.Equal(other.StartedAt) {
		return rec.StartedAt.Before(other.StartedAt)
	}
	return rec.PID < other.PID
}

// Running reports the newest run recorded for group and service that has not
// been asked to stop.
//
// It answers the start decision, which is a different question from List: a
// record with an accepted stop signal stays visible until its reap, because
// hiding a still-live process would be a lie. But that run is on its way out,
// and counting it as running would make a start — the start half of a restart
// in particular — report "already started" for the process option-berth just
// stopped. Newest first by the same order as List, so the run named here is
// the one the caller would otherwise have read.
func (r *Registry) Running(group, service string) (Record, bool) {
	r.Prune()
	r.mu.Lock()
	defer r.mu.Unlock()
	var newest Record
	found := false
	for _, rec := range r.runs {
		if rec.stopping || rec.stoppingReason != "" {
			continue
		}
		if rec.Group != group || rec.Name != service {
			continue
		}
		if !found || rec.StartedAt.After(newest.StartedAt) ||
			(rec.StartedAt.Equal(newest.StartedAt) && rec.PID > newest.PID) {
			newest = rec
			found = true
		}
	}
	return newest, found
}

// StopPending reports whether every run recorded for group and service has an
// accepted stop signal. It is the other half of Running: a start must not treat
// such a run as a live service, while the published service row keeps saying
// running until the exit is observed — a signal receipt is not an exit.
//
// The start decision reads two sources: this registry, and the snapshot's
// service row (which also covers listeners option-berth did not start). When the
// registry has already ruled a run out, that row must not overrule it, or the
// start half of a restart reports "already running" for the process the stop
// half just signalled.
func (r *Registry) StopPending(group, service string) bool {
	r.Prune()
	r.mu.Lock()
	defer r.mu.Unlock()
	found := false
	for _, rec := range r.runs {
		if rec.Group != group || rec.Name != service {
			continue
		}
		if !rec.stopping && rec.stoppingReason == "" {
			return false
		}
		found = true
	}
	return found
}

// Lookup returns the run registered for a pid.
func (r *Registry) Lookup(pid int) (Record, bool) {
	r.mu.Lock()
	rec, ok := r.runs[pid]
	r.mu.Unlock()
	return rec, ok
}

// Prune drops every run whose process has exited, and every run whose pid now
// belongs to a different process. The scanner calls it each tick; List and the
// resolver call it too, so a stale run never survives a read.
//
// The pid check matters for runs imported from runs.json after a daemon
// restart: those were not the daemon's children, so nothing reaps them and a
// recycled pid would otherwise keep a dead run "alive" — long enough for a
// later `down` to signal a process this tool never started.
func (r *Registry) Prune() {
	alive := r.Alive
	if alive == nil {
		alive = runs.PIDAlive
	}

	// Identity judgment happens outside the lock: StartTime may refresh the
	// scan's process table, and a registry read must not queue behind a `ps`.
	r.mu.Lock()
	live := make([]Record, 0, len(r.runs))
	for _, rec := range r.runs {
		live = append(live, rec)
	}
	r.mu.Unlock()

	// Retain the identity that was judged instead of a PID-only work list.
	// The compacted prefix below contains only generations actually removed.
	var dead []Record
	for _, rec := range live {
		if !alive(rec.PID) || r.pidReused(rec.PID, rec) {
			dead = append(dead, rec)
		}
	}
	if len(dead) == 0 {
		return
	}

	r.mirrorMu.Lock()
	defer r.mirrorMu.Unlock()
	r.mu.Lock()
	removed := dead[:0]
	for _, judged := range dead {
		if current, ok := r.runs[judged.PID]; ok && sameRun(current, judged) {
			delete(r.runs, judged.PID)
			removed = append(removed, judged)
		}
	}
	r.mu.Unlock()

	for _, rec := range removed {
		r.mirrorRemoveLocked(rec)
	}
}

// pidReused reports positive evidence that the pid now belongs to a different
// process than rec: the live process started clearly after the recorded run
// did. Missing evidence (no recorded time, no table answer, an unparsable
// lstart) is unknown, and unknown keeps the run — this never guesses a run
// dead.
func (r *Registry) pidReused(pid int, rec Record) bool {
	if rec.StartedAt.IsZero() || r.StartTime == nil {
		return false
	}
	started, ok := r.StartTime(pid)
	if !ok || started.IsZero() {
		return false
	}
	return started.After(rec.StartedAt.Add(ports.StartTolerance))
}

// Run implements groups.Registry: a listening port is attributed to the run
// that owns it by walking the port's PPID ancestry, so `npm run dev` -> vite ->
// esbuild all resolve to the run that started them.
//
// It answers with the whole run because the resolver stamps it onto the row:
// this registry is the daemon's authority on its own runs, and it knows a
// registration the moment `runs.register` returns.
func (r *Registry) Run(p state.Port) (state.Run, bool) {
	if rec, found := r.ancestor(p.PID, p.PPID); found {
		return state.Run{ID: rec.ID, Group: rec.Group, Name: rec.Name, RootPID: rec.PID}, true
	}
	return state.Run{}, false
}

// Live implements groups.Liveness for control-path dependency readiness.
// State publication instead captures all runs and exits with ServiceFacts.
func (r *Registry) Live(group, service string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.runs {
		if rec.stopping {
			continue
		}
		if rec.Group == group && rec.Name == service {
			return true
		}
	}
	return false
}

// GroupPIDs lists the live runs recorded under a group, so `oberth down` can
// stop the ones that hold no port — a worker, or a service still starting —
// which a kill by port never reaches.
func (r *Registry) GroupPIDs(group string) []int {
	var out []int
	for _, run := range r.GroupRuns(group) {
		out = append(out, run.RootPID)
	}
	return out
}

// GroupRuns lists the live runs recorded under a group with the service each
// belongs to. GroupPIDs is the projection of it that a stop needs.
func (r *Registry) GroupRuns(group string) []state.Run {
	var out []state.Run
	for _, rec := range r.List() {
		if rec.Group != group {
			continue
		}
		out = append(out, state.Run{ID: rec.ID, Group: rec.Group, Name: rec.Name, RootPID: rec.PID})
	}
	return out
}

// RunStart reports when the recorded process behind a live run pid started.
// The stop path hands it to the killer as the identity to verify before
// signalling; false means the registry has no start time, which is unknown,
// not evidence.
func (r *Registry) RunStart(pid int) (time.Time, bool) {
	rec, ok := r.Lookup(pid)
	if !ok || rec.StartedAt.IsZero() {
		return time.Time{}, false
	}
	return rec.StartedAt, true
}

// Session implements groups.SessionRegistry. It answers with the agent session
// behind the run that owns this port, found through the same PPID walk the
// attribution uses, so a port and its run can never disagree about who started
// them.
func (r *Registry) Session(p state.Port) (state.Session, bool) {
	var rec Record
	var found bool
	if p.Run != nil {
		// Attribution already captured an owner. Re-walking a live tree here
		// could attach a replacement's session to the previously captured run.
		rec, found = r.Lookup(p.Run.RootPID)
		if found && (rec.ID != p.Run.ID || rec.Group != p.Run.Group || rec.Name != p.Run.Name) {
			return state.Session{}, false
		}
	} else {
		// Standalone callers without captured attribution still resolve once.
		rec, found = r.ancestor(p.PID, p.PPID)
	}
	if found && rec.Session.ID != "" {
		return rec.Session, true
	}
	return state.Session{}, false
}

// SessionRuns lists every live run that carries a session. The daemon's
// sessions handlers read it through an interface assertion on the installed run
// registry, which is how package daemon reaches this data without importing the
// package that registers itself into it.
func (r *Registry) SessionRuns() []sessions.Live {
	out := []sessions.Live{}
	for _, rec := range r.List() {
		if rec.Session.ID != "" {
			out = append(out, live(rec))
		}
	}
	return out
}

// live projects one run onto the session view: the identity of the run, then
// what it was started as, then the session that asked for it.
func live(rec Record) sessions.Live {
	return sessions.Live{
		RunID: rec.ID,
		PID:   rec.PID,
		Group: rec.Group,
		Name:  rec.Name,
		Cmd:   rec.Cmd,
		Cwd:   rec.Cwd,
		// StartedAt is the run's, not the session's: a session outlives the
		// runs that carry it.
		StartedAt: rec.StartedAt,
		Session:   rec.Session,
	}
}

// ancestor walks up from pid looking for a registered run. hintPPID is the
// parent the scanner already resolved, used before the process table is read.
// The walk itself is ports.Ancestor, the same one the direct-scan registry
// uses: only the lookup source differs (this live registry, not runs.json).
func (r *Registry) ancestor(pid, hintPPID int) (Record, bool) {
	if pid <= 0 {
		return Record{}, false
	}
	// The port's own process first, then the parent the scanner resolved: both
	// are registry lookups, cheaper than reading the process table.
	if rec, ok := r.Lookup(pid); ok {
		return rec, true
	}
	// The parent the scanner already resolved is free evidence: it is checked
	// before the process table is read at all.
	if hintPPID > 1 {
		if rec, ok := r.Lookup(hintPPID); ok {
			return rec, true
		}
	}
	if r.isEmpty() {
		return Record{}, false
	}
	return ports.Ancestor(pid, r.parentTable(), r.Lookup)
}

// isEmpty reports whether nothing is registered at all. Walking the process
// table for a registry that knows no run can only come back empty, so the walk
// is skipped.
func (r *Registry) isEmpty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runs) == 0
}

func (r *Registry) clock() time.Time {
	if r.now == nil {
		return time.Now()
	}
	return r.now()
}
