package state

import (
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// StreamSnapshot is the redacted opening snapshot of a scoped subscription.
// It contains enough runtime evidence to identify the selected worktrees and
// decide whether to ask the worktree-local CLI for detail, but deliberately
// carries no commands, working directories, logs, or environment values.
type StreamSnapshot struct {
	Type          string           `json:"type"`
	Seq           uint64           `json:"seq"`
	StateRevision string           `json:"state_revision"`
	ObservedAt    string           `json:"observed_at"`
	Worktrees     []StreamWorktree `json:"worktrees"`
}

// StreamWorktree is the safe runtime projection for one selected checkout.
type StreamWorktree struct {
	Source   EventSource     `json:"source"`
	Status   string          `json:"status"`
	Services []StreamService `json:"services"`
	Ports    []StreamPort    `json:"ports"`
	Machine  []MachineRef    `json:"machine"`
}

// StreamService contains only service runtime facts. Manifest commands and
// paths remain available through the selected worktree's normal CLI. The
// executable-spec hashes are safe identity checks: they exclude environment
// values and let a filtered observer detect manifest/runtime drift without
// receiving a command or cwd.
type StreamService struct {
	Name                    string       `json:"name"`
	Running                 bool         `json:"running"`
	PortActual              *int         `json:"port_actual,omitempty"`
	PID                     *int         `json:"pid,omitempty"`
	RunID                   *string      `json:"run_id,omitempty"`
	StartedAt               *string      `json:"started_at,omitempty"`
	LastExit                *ServiceExit `json:"last_exit,omitempty"`
	HealthStatus            *Health      `json:"health_status,omitempty"`
	ManifestHash            *string      `json:"manifest_hash,omitempty"`
	RuntimeSpecHash         *string      `json:"runtime_spec_hash,omitempty"`
	ManifestRuntimeMismatch bool         `json:"manifest_runtime_mismatch"`
}

// StreamPort contains the runtime identity and health of a listener without
// the command line, cwd, group config, or container metadata.
type StreamPort struct {
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	BindAddress string   `json:"bind_address"`
	IPVersion   string   `json:"ip_version"`
	URL         string   `json:"url"`
	PID         int      `json:"pid"`
	PPID        int      `json:"ppid"`
	Process     string   `json:"process"`
	DisplayName string   `json:"display_name"`
	Name        *string  `json:"name,omitempty"`
	Type        PortType `json:"type"`
	RunID       string   `json:"run_id,omitempty"`
	Stats       *Stats   `json:"stats,omitempty"`
	Health      *Health  `json:"health,omitempty"`
	StartedAt   *string  `json:"started_at,omitempty"`
}

// StateChanged is the scoped notification. It is intentionally a summary:
// consumers use source/worktree identity and changed facts to decide whether
// to read the selected worktree's ordinary CLI status.
type StateChanged struct {
	Type          string       `json:"type"`
	Source        *EventSource `json:"source,omitempty"`
	EventID       string       `json:"event_id"`
	Seq           uint64       `json:"seq"`
	StateRevision string       `json:"state_revision"`
	Changed       []string     `json:"changed"`
	ObservedAt    string       `json:"observed_at"`
}

// StateRevisionForSeq is the stable textual revision shared by snapshots and
// changes. The daemon sequence remains the ordering primitive.
func StateRevisionForSeq(seq uint64) string { return strconv.FormatUint(seq, 10) }

// StreamSnapshotFor builds the redacted snapshot after scope filtering.
func StreamSnapshotFor(snap Snapshot, scope Scope) StreamSnapshot {
	snap = scope.FilterSnapshot(snap)
	out := StreamSnapshot{
		Type:          "state.snapshot",
		Seq:           snap.Seq,
		StateRevision: StateRevisionForSeq(snap.Seq),
		ObservedAt:    snap.At,
		Worktrees:     []StreamWorktree{},
	}

	bySource := make(map[string]int, len(snap.Groups))
	for _, g := range snap.Groups {
		source := EventSourceForGroup(g)
		row := StreamWorktree{
			Source:   *source,
			Status:   g.Status,
			Services: streamServices(g.Services),
			Ports:    []StreamPort{},
			Machine:  append([]MachineRef(nil), g.Machine...),
		}
		if row.Machine == nil {
			row.Machine = []MachineRef{}
		}
		idx := len(out.Worktrees)
		bySource[source.WorktreeID] = idx
		out.Worktrees = append(out.Worktrees, row)
	}

	for _, p := range snap.Ports {
		source := EventSourceForPort(p, snap.Groups)
		if source == nil {
			continue
		}
		idx, ok := bySource[source.WorktreeID]
		if !ok {
			out.Worktrees = append(out.Worktrees, StreamWorktree{
				Source:   *source,
				Status:   "running",
				Services: []StreamService{},
				Ports:    []StreamPort{},
				Machine:  []MachineRef{},
			})
			idx = len(out.Worktrees) - 1
			bySource[source.WorktreeID] = idx
		}
		out.Worktrees[idx].Ports = append(out.Worktrees[idx].Ports, streamPort(p))
	}

	for i := range out.Worktrees {
		sort.Slice(out.Worktrees[i].Services, func(a, b int) bool {
			return out.Worktrees[i].Services[a].Name < out.Worktrees[i].Services[b].Name
		})
		sort.Slice(out.Worktrees[i].Ports, func(a, b int) bool {
			left, right := out.Worktrees[i].Ports[a], out.Worktrees[i].Ports[b]
			if left.Port != right.Port {
				return left.Port < right.Port
			}
			return left.BindAddress < right.BindAddress
		})
	}
	sort.Slice(out.Worktrees, func(a, b int) bool {
		return out.Worktrees[a].Source.WorktreeID < out.Worktrees[b].Source.WorktreeID
	})
	return out
}

func streamServices(in []Service) []StreamService {
	out := make([]StreamService, 0, len(in))
	for _, service := range in {
		out = append(out, StreamService{
			Name:                    service.Name,
			Running:                 service.Running,
			PortActual:              service.PortActual,
			PID:                     service.PID,
			RunID:                   service.RunID,
			StartedAt:               service.StartedAt,
			LastExit:                service.LastExit,
			HealthStatus:            service.HealthStatus,
			ManifestHash:            service.ManifestHash,
			RuntimeSpecHash:         service.RuntimeSpecHash,
			ManifestRuntimeMismatch: service.ManifestRuntimeMismatch,
		})
	}
	if out == nil {
		return []StreamService{}
	}
	return out
}

func streamPort(p Port) StreamPort {
	runID := ""
	if p.Run != nil {
		runID = p.Run.ID
	}
	return StreamPort{
		Host:        p.Host,
		Port:        p.Port,
		BindAddress: p.BindAddress,
		IPVersion:   p.IPVersion,
		URL:         p.URL,
		PID:         p.PID,
		PPID:        p.PPID,
		Process:     p.Process,
		DisplayName: p.DisplayName,
		Name:        p.Name,
		Type:        p.Type,
		RunID:       runID,
		Stats:       p.Stats,
		Health:      p.Health,
		StartedAt:   p.StartedAt,
	}
}

// StateChangedForEvent converts one internal event into the public summary.
func StateChangedForEvent(ev Event, seq uint64, groups []Group) StateChanged {
	if seq == 0 {
		seq = ev.Seq
	}
	source := EventSourceForEvent(ev, groups)
	changed := eventChangeKind(ev.Kind)
	observed := ev.At
	eventID := strings.TrimSpace(ev.EventID)
	if eventID == "" {
		eventID = NewStateChangedID(seq, source, []string{changed})
	} else {
		eventID = OpaqueEventID(eventID)
	}
	return StateChanged{
		Type:          "state.changed",
		Source:        source,
		EventID:       eventID,
		Seq:           seq,
		StateRevision: StateRevisionForSeq(seq),
		Changed:       []string{changed},
		ObservedAt:    observed,
	}
}

func eventChangeKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case "port_up", "port_down", "port_restarted":
		return "port_changed"
	case "port_changed":
		return "port_changed"
	case "health_changed":
		return "health_changed"
	case "service_changed", "service_failed", "worktree_changed",
		"daemon_stopping", "scan_error", "db_reset",
		"port_occupied", "ready_timeout", "machine_unavailable",
		"undeclared_listener", "manifest_runtime_conflict", "repeated_crash":
		return strings.TrimSpace(kind)
	default:
		return "state_changed"
	}
}

func daemonWideChange(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "daemon_stopping", "scan_error", "db_reset":
		return true
	default:
		return false
	}
}

// StateChangesForTransition coalesces one scanner publish into one summary per
// affected source. It supplements the discrete port events with group/service
// changes so a portless worker can wake a related agent too.
func StateChangesForTransition(prev, next Snapshot, events []Event) []StateChanged {
	seq := next.Seq
	if seq == 0 {
		seq = prev.Seq
	}
	groups := append(append([]Group{}, prev.Groups...), next.Groups...)
	type bucket struct {
		source  *EventSource
		changed map[string]bool
		at      string
	}
	buckets := map[string]*bucket{}
	add := func(source *EventSource, kind, at string) {
		kind = eventChangeKind(kind)
		if source == nil && !daemonWideChange(kind) {
			return
		}
		key := "daemon"
		if source != nil {
			key = eventSourceKey(source)
		}
		b := buckets[key]
		if b == nil {
			b = &bucket{source: source, changed: map[string]bool{}, at: at}
			buckets[key] = b
		}
		if at != "" {
			b.at = at
		}
		b.changed[kind] = true
	}

	for _, ev := range events {
		source := EventSourceForEvent(ev, groups)
		add(source, ev.Kind, ev.At)
	}

	// A scoped summary is a runtime-change signal, not a resource sampler.
	// Ignore stats-only churn so a different subscriber's stats opt-in cannot
	// wake every integration workspace on every sample.
	d := Diff(prev, next)
	for _, p := range d.Ports.Added {
		add(EventSourceForPort(p, groups), "port_changed", next.At)
	}
	for _, p := range d.Ports.Updated {
		old, ok := portByKey(prev.Ports, p.Key())
		source := EventSourceForPort(p, groups)
		oldSource := EventSourceForPort(old, groups)
		if ok && streamHealthStatus(old.Health) != streamHealthStatus(p.Health) {
			add(source, "health_changed", next.At)
		}
		if !ok || portSummaryChanged(old, p) {
			add(source, "port_changed", next.At)
		}
		if ok && oldSource != nil && source != nil && oldSource.WorktreeID != source.WorktreeID {
			add(oldSource, "port_changed", next.At)
		}
	}
	for _, key := range d.Ports.Removed {
		if p, ok := portByKey(prev.Ports, key); ok {
			add(EventSourceForPort(p, groups), "port_changed", next.At)
		}
	}

	for _, g := range d.Groups.Added {
		add(EventSourceForGroup(g), "worktree_changed", next.At)
		addServiceChanges(nil, g.Services, EventSourceForGroup(g), next.At, add)
	}
	for _, g := range d.Groups.Updated {
		old, ok := groupByKey(prev.Groups, g.Key())
		if !ok {
			old = g
		}
		add(EventSourceForGroup(g), "worktree_changed", next.At)
		if WorktreeID(old) != WorktreeID(g) {
			// A moved checkout keeps its group name but changes the selected
			// worktree identity. Notify the old relation as well so it can
			// discard its row and resubscribe to the new root.
			add(EventSourceForGroup(old), "worktree_changed", next.At)
		}
		addServiceChanges(old.Services, g.Services, EventSourceForGroup(g), next.At, add)
	}
	for _, key := range d.Groups.Removed {
		if g, ok := groupByKey(prev.Groups, key); ok {
			add(EventSourceForGroup(g), "worktree_changed", next.At)
		}
	}

	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]StateChanged, 0, len(keys))
	for _, key := range keys {
		b := buckets[key]
		changed := make([]string, 0, len(b.changed))
		for kind := range b.changed {
			changed = append(changed, kind)
		}
		sort.Strings(changed)
		at := b.at
		if at == "" {
			at = next.At
		}
		out = append(out, StateChanged{
			Type:          "state.changed",
			Source:        b.source,
			EventID:       NewStateChangedID(seq, b.source, changed),
			Seq:           seq,
			StateRevision: StateRevisionForSeq(seq),
			Changed:       changed,
			ObservedAt:    at,
		})
	}
	return out
}

func streamHealthStatus(h *Health) string {
	if h == nil {
		return ""
	}
	return h.Status
}

func portSummaryChanged(a, b Port) bool {
	a.Stats = nil
	b.Stats = nil
	a.Health = nil
	b.Health = nil
	return !reflect.DeepEqual(a, b)
}

func addServiceChanges(before, after []Service, source *EventSource, at string, add func(*EventSource, string, string)) {
	old := make(map[string]Service, len(before))
	for _, service := range before {
		old[service.Name] = service
	}
	for _, service := range after {
		previous, existed := old[service.Name]
		if !existed || serviceRuntimeChanged(previous, service) {
			add(source, "service_changed", at)
		}
		if service.LastExit != nil && service.LastExit.Reason == "ready_timeout" &&
			(!existed || !sameExit(previous.LastExit, service.LastExit)) {
			add(source, "ready_timeout", at)
		}
		if serviceFailed(service) && (!existed || !serviceFailed(previous)) {
			add(source, "service_failed", at)
		}
		delete(old, service.Name)
	}
	if len(old) > 0 {
		add(source, "service_changed", at)
	}
}

func serviceRuntimeChanged(a, b Service) bool {
	return a.Running != b.Running || !sameIntPtr(a.PortActual, b.PortActual) ||
		!sameIntPtr(a.PID, b.PID) || !sameStringPtr(a.RunID, b.RunID) ||
		!sameStringPtr(a.StartedAt, b.StartedAt) || !sameExit(a.LastExit, b.LastExit)
}

func serviceFailed(s Service) bool {
	if s.LastExit == nil {
		return false
	}
	switch s.LastExit.Reason {
	case "crashed", "start_failed", "port_occupied", "dependency_timeout", "dependency_not_ready":
		return true
	default:
		return false
	}
}

func sameIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameExit(a, b *ServiceExit) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func portByKey(ports []Port, key string) (Port, bool) {
	for _, p := range ports {
		if p.Key() == key {
			return p, true
		}
	}
	return Port{}, false
}

func groupByKey(groups []Group, key string) (Group, bool) {
	for _, g := range groups {
		if g.Key() == key {
			return g, true
		}
	}
	return Group{}, false
}

func eventSourceKey(source *EventSource) string {
	if source == nil {
		return "daemon"
	}
	if source.WorktreeID != "" {
		return "worktree:" + source.WorktreeID
	}
	if source.RepoID != "" {
		return "repository:" + source.RepoID
	}
	if source.rootDir != "" {
		return "root:" + source.rootDir
	}
	return "source"
}
