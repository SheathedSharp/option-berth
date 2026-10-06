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

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/runs"
	"github.com/sheathedsharp/option-berth/internal/sessions"
	"github.com/sheathedsharp/option-berth/internal/state"
	"github.com/sheathedsharp/option-berth/internal/store"
)

// parentsTTL bounds how long one process table is reused while attributing a
// snapshot's worth of ports.
const parentsTTL = 2 * time.Second

// The PPID-walk bounds and cycle guards live with the process table, in
// ports.Ancestor; this registry only supplies the lookup.

// Record is one registered run.
type Record struct {
	ID        string
	PID       int
	PPID      int
	Group     string
	Name      string
	Cmd       string
	Cwd       string
	PortHint  int
	StartedAt time.Time
	// ConfigPath, StartID and Origin say where a run came from: the
	// oberth.yaml it was started from, the groups.start that started it with
	// its siblings, and the client that asked for it (cli, app, mcp).
	ConfigPath string
	StartID    string
	Origin     string
	SpecHash   string
	// LogPath and LogOffset are where a detached run's output goes and where
	// this run's part of the file begins; its last lines are kept on exit.
	LogPath   string
	LogOffset int64
	// Session is the agent session that asked for this run, or the zero value
	// when nothing did (spec 2 §3). It travels with the run so every port the
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
	if existing, ok := r.runs[rec.PID]; ok && rec.ID == "" {
		rec.ID = existing.ID
	}
	r.runs[rec.PID] = rec
	r.mu.Unlock()

	r.mirrorAddLocked(rec)
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
	}
	return ok
}

// RenameGroups moves every run recorded under an old group name to its new one
// (`groups.rename`), so a service started before its project was renamed stays
// in the project's group instead of keeping a group of the old name to itself.
// It reports how many runs moved.
func (r *Registry) RenameGroups(renames map[string]string) int {
	if len(renames) == 0 {
		return 0
	}
	r.mirrorMu.Lock()
	defer r.mirrorMu.Unlock()
	r.mu.Lock()
	var moved []Record
	for pid, rec := range r.runs {
		next, ok := renames[rec.Group]
		if !ok || next == "" {
			continue
		}
		rec.Group = next
		r.runs[pid] = rec
		moved = append(moved, rec)
	}
	for i := range r.exits {
		if next, ok := renames[r.exits[i].Group]; ok && next != "" {
			r.exits[i].Group = next
		}
	}
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

// List returns the live runs, oldest first, after pruning dead ones.
func (r *Registry) List() []Record {
	r.Prune()
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Record, 0, len(r.runs))
	for _, rec := range r.runs {
		if rec.stopping {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].PID < out[j].PID
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}

// Lookup returns the run registered for a pid.
func (r *Registry) Lookup(pid int) (Record, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[pid]
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

// Run implements groups.Registry: it attributes a listening port to the run
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

// Session implements groups.SessionRegistry: it reports the agent session that
// started the run owning this port, using the same PPID walk the run
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
	if !found || rec.Session.ID == "" {
		return state.Session{}, false
	}
	return rec.Session, true
}

// SessionRuns lists every live run that carries a session. The daemon's
// sessions handlers read it through an interface assertion on the installed
// run registry, which is how package daemon reaches this data without
// importing the package that registers itself into it.
func (r *Registry) SessionRuns() []sessions.Live {
	out := []sessions.Live{}
	for _, rec := range r.List() {
		if rec.Session.ID == "" {
			continue
		}
		out = append(out, sessions.Live{
			RunID:     rec.ID,
			PID:       rec.PID,
			Group:     rec.Group,
			Name:      rec.Name,
			Cmd:       rec.Cmd,
			Cwd:       rec.Cwd,
			StartedAt: rec.StartedAt,
			Session:   rec.Session,
		})
	}
	return out
}

// ancestor walks up from pid looking for a registered run. hintPPID is the
// parent the scanner already resolved, used before the process table is read.
// The walk itself is ports.Ancestor, the same one the direct-scan registry
// uses: only the lookup source differs (this live registry, not runs.json).
func (r *Registry) ancestor(pid, hintPPID int) (Record, bool) {
	if pid <= 0 {
		return Record{}, false
	}
	if rec, ok := r.Lookup(pid); ok {
		return rec, true
	}
	if hintPPID > 1 {
		if rec, ok := r.Lookup(hintPPID); ok {
			return rec, true
		}
	}

	r.mu.Lock()
	empty := len(r.runs) == 0
	r.mu.Unlock()
	if empty {
		return Record{}, false
	}

	return ports.Ancestor(pid, r.parentTable(), r.Lookup)
}

func (r *Registry) clock() time.Time {
	if r.now == nil {
		return time.Now()
	}
	return r.now()
}
