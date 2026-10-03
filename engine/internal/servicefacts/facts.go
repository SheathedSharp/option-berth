// Package servicefacts reduces declared services and observed runtime facts.
// It performs no I/O and owns no persistent state: each caller supplies one
// registry snapshot and one listener batch for a single publication.
package servicefacts

import (
	"time"

	"github.com/sheathedsharp/option-berth/internal/state"
)

// Key scopes a service name to its resolved worktree group.
type Key struct {
	Group   string
	Service string
}

// RunRecord is a projection of an existing registry entry, not a second run
// registry. Stopping records still reserve their identity and suppress old exits.
type RunRecord struct {
	Key      Key
	Run      state.ServiceRun
	PortHint int
	Stopping bool
	// StartedAt retains source precision for overlapping runs without changing
	// the published Run.StartedAt string. Zero falls back to parsing that string.
	StartedAt time.Time
}

type ExitRecord struct {
	Key  Key
	Exit state.ServiceExit
}

// Evidence belongs to one service in one capture. A stopping Run is not live,
// but its listener may remain observable until the process actually exits.
type Evidence struct {
	Run       *state.ServiceRun
	PortHint  int
	Stopping  bool
	LastExit  *state.ServiceExit
	startedAt time.Time
}

// Snapshot is an ephemeral, detached view of registry facts. Treat it as
// read-only after Capture; it is never written back to the registry.
type Snapshot map[Key]Evidence

// Provider captures all service facts once, rather than querying the registry
// separately for each service and each kind of evidence.
type Provider interface {
	ServiceFacts() Snapshot
}

// Capture detaches facts from their inputs. Exits must be newest first, as in
// both the daemon history and the direct reader's store query. Overlapping live
// runs select the newest non-stopping run, with stable identity tie-breakers;
// a port hint always belongs to that same selected run.
func Capture(runs []RunRecord, exits []ExitRecord) Snapshot {
	collector := NewCollector(len(runs) + len(exits))
	for _, record := range runs {
		collector.AddRun(record)
	}
	for _, record := range exits {
		collector.AddExit(record)
	}
	return collector.Snapshot()
}

// Collector builds one detached snapshot without materializing projection
// arrays. Callers hold their source lock while adding records; no source
// pointers escape. The zero value is usable, but not safe for concurrent writes.
type Collector struct {
	facts Snapshot
}

func NewCollector(capacity int) Collector {
	return Collector{facts: make(Snapshot, capacity)}
}

func (c *Collector) AddRun(record RunRecord) {
	if record.Key.Group == "" || record.Key.Service == "" {
		return
	}
	if record.StartedAt.IsZero() {
		record.StartedAt, _ = time.Parse(time.RFC3339Nano, record.Run.StartedAt)
	}
	if previous := c.facts[record.Key]; previous.Run != nil && !preferRun(record, previous) {
		return
	}
	if c.facts == nil {
		c.facts = make(Snapshot)
	}
	run := record.Run
	c.facts[record.Key] = Evidence{Run: &run, PortHint: record.PortHint, Stopping: record.Stopping, startedAt: record.StartedAt}
}

// AddExit consumes newest-first history. Any registered run, including one
// being stopped, takes precedence even if it is added after the exit.
func (c *Collector) AddExit(record ExitRecord) {
	if record.Key.Group == "" || record.Key.Service == "" {
		return
	}
	if _, exists := c.facts[record.Key]; exists {
		return
	}
	if c.facts == nil {
		c.facts = make(Snapshot)
	}
	exit := record.Exit
	c.facts[record.Key] = Evidence{LastExit: &exit}
}

// Snapshot transfers the collected map. Reusing the collector starts a new
// map rather than mutating any previously published snapshot.
func (c *Collector) Snapshot() Snapshot {
	out := c.facts
	c.facts = nil
	return out
}

func preferRun(candidate RunRecord, previous Evidence) bool {
	if candidate.Stopping != previous.Stopping {
		return !candidate.Stopping
	}
	if !candidate.StartedAt.Equal(previous.startedAt) {
		return candidate.StartedAt.After(previous.startedAt)
	}
	if candidate.Run.ID != previous.Run.ID {
		return candidate.Run.ID > previous.Run.ID
	}
	return candidate.Run.PID > previous.Run.PID
}

type Declaration struct {
	Key      Key
	Port     int
	PortAuto bool
}

// Result references read-only evidence from the supplied batch. Running means
// a live registered run OR an eligible observed listener, not proof of process
// ownership. Only Run carries registry-owned identity.
type Result struct {
	Running  bool
	Run      *state.ServiceRun
	Listener *state.Port
	LastExit *state.ServiceExit
}

type portKey struct {
	group string
	port  int
}

// Summarize starts with declared services and their run evidence, then routes
// listeners to candidate services through scoped indexes. It never discovers
// services or infers run ownership from a port number. The first eligible
// listener in input order wins, preserving the public join's tie-breaker.
func Summarize(declarations []Declaration, facts Snapshot, listeners []state.Port) []Result {
	resolver := NewResolver(facts, listeners)
	out := make([]Result, len(declarations))
	for i, declaration := range declarations {
		out[i] = resolver.Resolve(declaration)
	}
	return out
}

// Resolver is a read-only index for one listener batch. Index memory scales
// with observations, not declarations: thousands of portless workers need no
// listener index at all. Next links use one-based offsets; zero ends a chain.
type Resolver struct {
	facts                       Snapshot
	listeners                   []state.Port
	byPort                      map[portKey]int
	byName                      map[Key]int
	byRun                       map[Key]int
	nextPort, nextName, nextRun []int
}

func NewResolver(facts Snapshot, listeners []state.Port) Resolver {
	r := Resolver{facts: facts, listeners: listeners}
	if len(listeners) == 0 {
		return r
	}
	r.byPort = make(map[portKey]int, len(listeners))
	r.nextPort = make([]int, len(listeners))
	for i := len(listeners) - 1; i >= 0; i-- {
		listener := &listeners[i]
		if listener.Group == nil || *listener.Group == "" {
			continue
		}
		group := *listener.Group
		port := portKey{group, listener.Port}
		r.nextPort[i], r.byPort[port] = r.byPort[port], i+1
		if listener.DisplayName != "" {
			if r.byName == nil {
				r.byName = make(map[Key]int, len(listeners))
				r.nextName = make([]int, len(listeners))
			}
			name := Key{group, listener.DisplayName}
			r.nextName[i], r.byName[name] = r.byName[name], i+1
		}
		if listener.Run != nil && listener.Run.Name != "" {
			if r.byRun == nil {
				r.byRun = make(map[Key]int, len(listeners))
				r.nextRun = make([]int, len(listeners))
			}
			name := Key{group, listener.Run.Name}
			r.nextRun[i], r.byRun[name] = r.byRun[name], i+1
		}
	}
	return r
}

// Resolve computes one declared service without allocating a request/result
// slice. It never mutates the snapshot, observations, or reusable index.
func (r Resolver) Resolve(declaration Declaration) Result {
	evidence := r.facts[declaration.Key]
	out := Result{}
	if evidence.Run != nil && !evidence.Stopping {
		out.Run, out.Running = evidence.Run, true
	} else if evidence.Run == nil {
		out.LastExit = evidence.LastExit
	}
	best := len(r.listeners) + 1
	consider := func(at int, next []int) {
		for at != 0 && at < best {
			if eligible(declaration.Key, evidence, &r.listeners[at-1]) {
				best = at
				return
			}
			at = next[at-1]
		}
	}
	if declaration.Port != 0 {
		consider(r.byPort[portKey{declaration.Key.Group, declaration.Port}], r.nextPort)
	}
	if declaration.PortAuto && evidence.PortHint != 0 {
		consider(r.byPort[portKey{declaration.Key.Group, evidence.PortHint}], r.nextPort)
	}
	consider(r.byName[declaration.Key], r.nextName)
	consider(r.byRun[declaration.Key], r.nextRun)
	if best <= len(r.listeners) {
		out.Listener, out.Running, out.LastExit = &r.listeners[best-1], true, nil
	}
	return out
}

func eligible(key Key, evidence Evidence, listener *state.Port) bool {
	owner := listener.Run
	if owner == nil {
		return true // observed listener, without claiming a managed run
	}
	if (owner.Group != "" && owner.Group != key.Group) || (owner.Name != "" && owner.Name != key.Service) {
		return false
	}
	if owner.ID != "" {
		if evidence.Run != nil && evidence.Run.ID != "" && owner.ID != evidence.Run.ID {
			return false // a listener from a different generation is not this run
		}
		if evidence.LastExit != nil && owner.ID == evidence.LastExit.RunID {
			return false // stale listener evidence cannot resurrect a finished run
		}
	}
	return true
}
