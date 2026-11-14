package sessions

import (
	"sort"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// Live is one running run that belongs to a session, as the runtime service's run
// registry reports it. The whole run travels rather than its id because
// `sessions.inspect` lists a session's runs from here; there is no second
// registry to consult.
type Live struct {
	Session state.Session

	// How the registry knows the run.
	RunID string
	PID   int

	// Where the manifest placed it.
	Group string
	Name  string

	// What was launched and when.
	Cmd       string
	Cwd       string
	StartedAt time.Time
}

// Known is a session the store remembers, whether or not it is still live.
// Inactive sessions stay for the retention window so port history can still
// name the agent that opened a port long after it closed.
type Known struct {
	Session   state.Session
	FirstSeen time.Time
	LastSeen  time.Time
}

// Retention is how long an inactive session survives before Prune drops it.
const Retention = 7 * 24 * time.Hour

// tally collects the three sources that may mention the same session and
// folds them into one record per id. Insertion order is kept separately from
// the map so the output does not depend on map iteration.
type tally struct {
	ids    []string
	byID   map[string]*state.SessionRecord
	groups map[string]map[string]struct{}
}

func newTally() *tally {
	return &tally{
		byID:   make(map[string]*state.SessionRecord),
		groups: make(map[string]map[string]struct{}),
	}
}

// record returns the session's record, creating it on first mention.
func (t *tally) record(s state.Session) *state.SessionRecord {
	if existing, seen := t.byID[s.ID]; seen {
		return existing
	}
	fresh := &state.SessionRecord{Session: s}
	t.byID[s.ID] = fresh
	t.ids = append(t.ids, s.ID)
	return fresh
}

// markGroup remembers that a session touched a named group; the record
// publishes how many distinct groups that amounts to.
func (t *tally) markGroup(id, group string) {
	if group == "" {
		return
	}
	seen := t.groups[id]
	if seen == nil {
		seen = make(map[string]struct{})
		t.groups[id] = seen
	}
	seen[group] = struct{}{}
}

// snapshot finalises counts and order. Active sessions lead, then the most
// recently seen, then id: a stable order keeps consecutive ticks from
// manufacturing deltas out of nothing.
func (t *tally) snapshot() []state.SessionRecord {
	out := make([]state.SessionRecord, 0, len(t.ids))
	for _, id := range t.ids {
		rec := t.byID[id]
		rec.Groups = len(t.groups[id])
		out = append(out, *rec)
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i], out[j]
		if left.Active != right.Active {
			return left.Active
		}
		if left.LastSeen != right.LastSeen {
			return left.LastSeen > right.LastSeen
		}
		return left.ID < right.ID
	})
	return out
}

// Records builds the snapshot's `sessions` collection from what the store
// remembers, what the registry reports as live, and the ports this tick
// attributed.
//
// Activity is defined by live runs, not by ports: an agent whose dev server is
// still binding, or whose command binds nothing at all, is active all the same.
func Records(known []Known, live []Live, ports []state.Port) []state.SessionRecord {
	t := newTally()

	for _, remembered := range known {
		if remembered.Session.ID == "" {
			continue
		}
		rec := t.record(remembered.Session)
		rec.FirstSeen = stamp(remembered.FirstSeen)
		rec.LastSeen = stamp(remembered.LastSeen)
	}

	for _, run := range live {
		if run.Session.ID == "" {
			continue
		}
		rec := t.record(run.Session)
		// The live identity wins over the stored one: a rebase changes the
		// branch a session is on without changing its id.
		rec.Session = run.Session
		rec.Runs++
		rec.Active = true
		if rec.FirstSeen == "" {
			rec.FirstSeen = stamp(run.StartedAt)
		}
		t.markGroup(run.Session.ID, run.Group)
	}

	for i := range ports {
		owner := ports[i].Session
		if owner == nil || owner.ID == "" {
			continue
		}
		rec, seen := t.byID[owner.ID]
		if !seen {
			rec = t.record(*owner)
			// A port stamped with an unknown session proves the session was
			// active even if the registry has already forgotten the run.
			rec.Active = true
		}
		rec.Ports++
		if group := ports[i].Group; group != nil {
			t.markGroup(owner.ID, *group)
		}
	}

	return t.snapshot()
}

// RunsOf returns one session's live runs, oldest first.
func RunsOf(live []Live, id string) []Live {
	var out []Live
	for _, run := range live {
		if run.Session.ID == id {
			out = append(out, run)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}

// PortsOf returns the ports attributed to one session. No matches yields an
// empty slice, not nil, so it encodes as [] on the wire.
func PortsOf(ports []state.Port, id string) []state.Port {
	out := []state.Port{}
	for i := range ports {
		if owner := ports[i].Session; owner != nil && owner.ID == id {
			out = append(out, ports[i])
		}
	}
	return out
}

// stamp formats a timestamp for the wire, leaving an unknown time empty.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
