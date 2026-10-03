package scanner

import (
	"context"
	"os"
	"time"

	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
	"github.com/sheathedsharp/option-berth/internal/store"
)

// Store is the slice of the daemon's SQLite store the scan tick uses: the
// `oberth.yaml` roots it remembers and the history ring it appends to.
// It is an interface so the loop can run without a database — the CLI's
// direct-scan path and most tests do.
type Store interface {
	Roots() ([]string, error)
	AddRoot(path string) error
	// RemoveRoot is the other half of AddRoot: the seed step drops the roots
	// whose directory is no longer there (see seedRoots).
	RemoveRoot(path string) error
	AppendBatch(events []store.HistoryEvent) error
	GroupAliases() (map[string]string, error)
}

// attribution is the per-loop group state: the index of known `oberth.yaml`
// files, which lives as long as the daemon, and the roots already written to
// the store.
type attribution struct {
	mu     contextMutex
	index  *groups.Index
	seeded bool
	roots  map[string]bool
}

// SetStore installs the store after construction. The daemon does it from
// Serve, once the database is open; a nil store leaves the loop unpersisted.
func (l *Loop) SetStore(s Store) {
	if s == nil {
		return
	}
	l.attr.mu.Lock()
	defer l.attr.mu.Unlock()
	l.opts.Store = s
	l.attr.seeded = false
	l.attr.roots = map[string]bool{} // acknowledgements belong to this store
}

// SetRuns installs the run-registry accessor. It is a function because
// `oberth start` (step 1A.5) hands the daemon its registry after the loop is
// already scanning.
func (l *Loop) SetRuns(f func() groups.Registry) {
	if f == nil {
		return
	}
	l.attr.mu.Lock()
	defer l.attr.mu.Unlock()
	l.opts.Runs = f
}

// SetSessions installs the sessions-collection builder. Like SetRuns it is a
// setter because the daemon extension that owns sessions registers itself
// after the loop is already scanning.
func (l *Loop) SetSessions(f func(ports []state.Port) []state.SessionRecord) {
	if f == nil {
		return
	}
	l.attr.mu.Lock()
	defer l.attr.mu.Unlock()
	l.opts.Sessions = f
}

// sessions builds the snapshot's sessions collection, never nil.
func (l *Loop) sessions(rows []state.Port) []state.SessionRecord {
	out, _ := l.sessionsContext(context.Background(), rows)
	return out
}

func (l *Loop) sessionsContext(ctx context.Context, rows []state.Port) ([]state.SessionRecord, error) {
	if err := l.attr.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	build := l.opts.Sessions
	l.attr.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []state.SessionRecord{}
	if build != nil {
		out = build(rows)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []state.SessionRecord{}
	}
	return out, nil
}

// Invalidate drops the cached scan so the next read rescans instead of serving
// state from before a write. The config and rename writes call it so the caller
// sees its own change in the very next list.
func (l *Loop) Invalidate() {
	l.mu.Lock()
	l.refreshRequested++
	l.lastScanAt = time.Time{}
	l.interval = l.base
	l.mu.Unlock()
	l.Wake()
}

// attribute is the group half of a scan tick. It resolves every port's group,
// remembers any newly seen `oberth.yaml` root and builds the group
// collection.
func (l *Loop) attribute(pp []ports.ListeningPort) ([]state.Port, []state.Group) {
	rows, groups, _ := l.attributeContext(context.Background(), pp)
	return rows, groups
}

// attributeContext cancels admission and checks between synchronous stages.
// Store and filesystem calls already running must return. Completed index/root
// observations are not rolled back when the enclosing publication is cancelled.
func (l *Loop) attributeContext(ctx context.Context, pp []ports.ListeningPort) ([]state.Port, []state.Group, error) {
	if err := l.attr.mu.LockContext(ctx); err != nil {
		return nil, nil, err
	}
	defer l.attr.mu.Unlock()

	if l.attr.index == nil {
		l.attr.index = groups.NewIndex()
		l.attr.roots = map[string]bool{}
	}
	st := l.opts.Store
	l.seedRoots(st)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	l.loadAliases(st)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	reg := l.registry()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	resolved, index := groups.AttributeWith(pp, reg, l.attr.index)
	l.attr.index = index
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	l.rememberRoots(st, index)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	// A registry that knows the ports it assigned lets a `port: auto` service
	// join its listener even when that listener is not in its process tree,
	// and one that remembers exits lets a service that is down say it crashed.
	groupRows := groups.GroupsWith(resolved, index, reg)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return resolved, groupRows, nil
}

// seedRoots loads the known `oberth.yaml` roots into the index once, so a
// project configured before the last restart is a group again immediately,
// without waiting for one of its processes to be seen.
//
// A root whose directory is gone is dropped from the store on the way in.
// Nothing else ever removes one — rememberRoots only adds — so without this a
// project that was deleted or moved is remembered forever and re-seeded on
// every daemon start: a demo checkout that moved, or a test's temporary
// directory, which is how dead paths end up in a real database.
func (l *Loop) seedRoots(st Store) {
	if st == nil || l.attr.seeded {
		return
	}
	roots, err := st.Roots()
	if err != nil {
		l.opts.Logger.Warn("reading known oberth.yaml roots", "error", err)
		return
	}
	complete := true
	var discovered []string
	for _, root := range roots {
		info, err := os.Stat(root)
		if os.IsNotExist(err) || (err == nil && !info.IsDir()) {
			if err := st.RemoveRoot(root); err != nil {
				complete = false
				l.opts.Logger.Warn("forgetting a vanished oberth.yaml root", "dir", root, "error", err)
			} else {
				delete(l.attr.roots, root)
			}
			continue
		}
		if err != nil {
			// A denied or temporarily unavailable directory is not evidence
			// of deletion. Keep its persistent record and retry on a later tick.
			complete = false
			l.opts.Logger.Warn("reading oberth.yaml root; keeping its record", "dir", root, "error", err)
			continue
		}
		if !l.attr.roots[root] {
			discovered = append(discovered, root)
			l.attr.roots[root] = true
		}
	}
	if len(discovered) > 0 {
		// Retain explicit roots even before they have a manifest; a silent
		// project must become visible when its first declaration is created.
		l.attr.index.Reload(discovered)
	}
	// Retry failures on the existing scan cadence, never in a busy loop or
	// a new worker. Successful roots are not re-observed during that retry.
	l.attr.seeded = complete
}

// loadAliases hands the index this tick's project names from `groups.rename`.
// It runs under the ordering gate, so a republish after a rename always resolves
// with the name it just stored. A failed read keeps the
// last tick's names rather than flapping every renamed project back to its
// directory name.
func (l *Loop) loadAliases(st Store) {
	if st == nil {
		l.attr.index.SetAliases(nil)
		return
	}
	aliases, err := st.GroupAliases()
	if err != nil {
		l.opts.Logger.Warn("reading project aliases", "error", err)
		return
	}
	l.attr.index.SetAliases(aliases)
}

// registry is the run registry to attribute with: the one `oberth start`
// installed, or the attribution the scanner already put on the port itself.
func (l *Loop) registry() groups.Registry {
	if l.opts.Runs == nil {
		return groups.PortRuns{}
	}
	if r := l.opts.Runs(); r != nil {
		return r
	}
	return groups.PortRuns{}
}

// rememberRoots persists a `oberth.yaml` directory the first time it is seen,
// so the next daemon start knows about the project without walking the disk.
func (l *Loop) rememberRoots(st Store, index *groups.Index) {
	if st == nil {
		return
	}
	for _, cfg := range index.Configs() {
		if cfg == nil || cfg.Dir == "" || l.attr.roots[cfg.Dir] {
			continue
		}
		if err := st.AddRoot(cfg.Dir); err != nil {
			l.opts.Logger.Warn("recording a oberth.yaml root", "dir", cfg.Dir, "error", err)
			continue
		}
		l.attr.roots[cfg.Dir] = true
	}
}

// record writes the port transitions of one published delta to the history
// ring. Only the three port-scoped kinds are persisted; health_changed and
// scan_error stay in-memory notifications.
func (l *Loop) record(events []state.Event) {
	st := l.store()
	if st == nil || len(events) == 0 {
		return
	}
	rows := make([]store.HistoryEvent, 0, len(events))
	for _, ev := range events {
		if ev.Port == nil {
			continue
		}
		switch ev.Kind {
		case store.EventPortUp, store.EventPortDown, store.EventPortRestarted:
		default:
			continue
		}
		p := *ev.Port
		rows = append(rows, store.HistoryEvent{
			At:          eventTime(ev.At, l.now()),
			Kind:        ev.Kind,
			Port:        p.Port,
			PID:         p.PID,
			DisplayName: p.DisplayName,
			Group:       deref(p.Group),
			Bind:        p.BindAddress,
			ProjectRoot: deref(p.ProjectRoot),
			Command:     p.Command,
		})
	}
	if err := st.AppendBatch(rows); err != nil {
		l.opts.Logger.Warn("recording port history", "error", err)
	}
}

// store returns the installed store under the attribution lock.
func (l *Loop) store() Store {
	l.attr.mu.Lock()
	defer l.attr.mu.Unlock()
	return l.opts.Store
}

func eventTime(at string, fallback time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		return t
	}
	return fallback
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
