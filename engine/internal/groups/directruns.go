package groups

import (
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/runs"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// DirectRuns resolves run ownership without a daemon: the on-disk runs.json
// registry plus the process table the scan already read. It is the direct-scan
// counterpart of the daemon's in-memory registry, and it is what keeps run
// attribution in the attribution layer: scanning reads process facts,
// attribution decides which run owns them.
//
// One instance serves one publication round. The caches are per round and
// never outlive the process table they were built from, so a recycled pid
// cannot inherit an earlier round's answer.
type DirectRuns struct {
	reg     *runs.Registry
	parents map[int]int
	// startedAt reports when a pid's current process began. It is the same
	// identity check the daemon registry prunes with; tests replace it.
	startedAt func(pid int) (time.Time, bool)
	// resolved memoizes whole walk results per pid: every pid visited on a
	// walk shares its endpoint, so sibling listeners in one process tree are
	// answered from the first walk.
	resolved map[int]runHit
}

type runHit struct {
	run   state.Run
	ok    bool
	depth int // nodes from this PID to its owner, inclusive; zero for a miss
}

// NewDirectRuns loads the registry and the parent table.
func NewDirectRuns() *DirectRuns {
	return newDirectRuns(runs.Load(), ports.ParentTable())
}

func newDirectRuns(reg *runs.Registry, parents map[int]int) *DirectRuns {
	return &DirectRuns{reg: reg, parents: parents, startedAt: ports.ProcessStart, resolved: map[int]runHit{}}
}

// Run implements Registry by walking the listener's ancestry to the nearest
// pid the registry knows, the same walk the daemon's registry uses.
func (d *DirectRuns) Run(p state.Port) (state.Run, bool) {
	return d.runForPID(p.PID)
}

// StampRuns writes run ownership onto scanner rows that are about to be
// rendered without building the group collection. The collection layer no
// longer knows about runs, so one-off reads (`logs <port>`, shell completion)
// call this before rendering; full reads get the same fields from Attribute.
func StampRuns(pp []ports.ListeningPort) {
	stampRuns(pp, NewDirectRuns())
}

func (d *DirectRuns) runForPID(pid int) (state.Run, bool) {
	if d == nil || d.reg == nil || len(d.reg.Runs) == 0 || pid <= 1 {
		return state.Run{}, false
	}
	if hit, ok := d.resolved[pid]; ok {
		return hit.run, hit.ok
	}
	return d.resolveRunForPID(pid)
}

// Keep the bounded scratch frame off the high-frequency memo-hit path.
// The caller has already checked the registry and the starting PID cache.
func (d *DirectRuns) resolveRunForPID(pid int) (state.Run, bool) {
	// Ancestor invokes the visitor at most MaxAncestry times. Keep scratch
	// bounded to this call; no pooled state or cache survives a publication.
	var path [ports.MaxAncestry]int
	visited := path[:0]
	// The walk's bool means "resolved", not "owned": a cached negative is
	// terminal too. Positive cache entries carry distance so warming a cache
	// cannot make a later query jump beyond MaxAncestry.
	hit, resolved := ports.Ancestor(pid, d.parents, func(pid int) (runHit, bool) {
		if cached, ok := d.resolved[pid]; ok {
			return cached, true
		}
		visited = append(visited, pid)
		e, found := d.reg.LookupByPID(pid)
		if !found || d.entryReused(pid, e) {
			return runHit{}, false
		}
		return runHit{run: state.Run{ID: e.ID, Group: e.GroupOf(), Name: e.NameOf(), RootPID: e.PID}, ok: true}, true
	})
	if !resolved && len(visited) >= ports.MaxAncestry {
		// A truncated walk proves absence only for its starting PID. Ancestors
		// have their own full budget and may still reach an owner above it.
		d.resolved[pid] = runHit{}
		return state.Run{}, false
	}
	for i := len(visited) - 1; i >= 0; i-- {
		if hit.ok {
			hit.depth++
			if hit.depth > ports.MaxAncestry {
				hit = runHit{}
			}
		}
		d.resolved[visited[i]] = hit
	}
	hit = d.resolved[pid]
	return hit.run, hit.ok
}

// entryReused reports positive evidence that pid now belongs to a different
// process than the registry entry records: the live process started clearly
// after the entry did. Missing evidence — no recorded time, no live answer, an
// unparsable timestamp — keeps the entry; this never guesses.
func (d *DirectRuns) entryReused(pid int, e runs.Entry) bool {
	if e.StartedAt == "" || d.startedAt == nil {
		return false
	}
	recorded, err := time.Parse(time.RFC3339, e.StartedAt)
	if err != nil {
		return false
	}
	live, ok := d.startedAt(pid)
	if !ok || live.IsZero() {
		return false
	}
	return live.After(recorded.Add(ports.StartTolerance))
}
