package groups

import (
	"slices"
	"sort"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// Groups builds the group collection from resolved ports. Every group that at
// least one port resolves to appears, plus every valid `oberth.yaml` the index
// knows about — a project whose services are all down is still a group, it is
// just stopped.
//
// index may be nil, in which case groups carry no services and no config path.
func Groups(pp []state.Port, index *Index) []state.Group { return GroupsWith(pp, index, nil, false) }

// Liveness is the control path's readiness check for a portless dependency.
// State publication instead consumes one servicefacts.Provider snapshot.
type Liveness interface {
	Live(group, service string) bool
}

// groupTable is the collection being built, keyed by group name, with the
// member ports already added so a listener on two bind addresses counts once.
type groupTable struct {
	byName  map[string]*state.Group
	members map[string]map[int]struct{}
}

func newGroupTable() *groupTable {
	return &groupTable{
		byName:  map[string]*state.Group{},
		members: map[string]map[int]struct{}{},
	}
}

// get returns the named group, creating the empty row on first sight.
func (t *groupTable) get(name string) *state.Group {
	if g, ok := t.byName[name]; ok {
		return g
	}
	g := &state.Group{Name: name, Source: state.SourceAuto, Members: []int{}, Services: []state.Service{}, Machine: []state.MachineRef{}}
	t.byName[name] = g
	return g
}

// addMember records a listener's port on its group once.
func (t *groupTable) addMember(g *state.Group, port int) {
	seen := t.members[g.Name]
	if seen == nil {
		seen = map[int]struct{}{}
		t.members[g.Name] = seen
	}
	if _, duplicate := seen[port]; duplicate {
		return
	}
	seen[port] = struct{}{}
	g.Members = append(g.Members, port)
}

// GroupsWith captures runtime facts once when reg implements
// servicefacts.Provider. Otherwise only observed listeners are available.
//
// narrow says the listener table these rows came from cannot cover every
// socket owner: a `machine:` port missing from it is then unknown, not absent.
func GroupsWith(pp []state.Port, index *Index, reg Registry, narrow bool) []state.Group {
	if index == nil {
		index = NewIndex()
	}
	pass := newResolutionPass(index)
	var facts servicefacts.Snapshot
	if provider, ok := reg.(servicefacts.Provider); ok {
		facts = provider.ServiceFacts()
	}
	table := newGroupTable()
	machinePorts := make(map[int]struct{}, len(pp))
	for _, p := range pp {
		machinePorts[p.Port] = struct{}{}
	}

	for _, p := range pp {
		if p.Group == nil || *p.Group == "" {
			continue
		}
		g := table.get(*p.Group)
		if p.GroupSource != nil && rank(*p.GroupSource) > rank(g.Source) {
			g.Source = *p.GroupSource
		}
		table.addMember(g, p.Port)
		if g.RootDir == nil && p.ProjectRoot != nil {
			root := *p.ProjectRoot
			g.RootDir = &root
		}
	}

	// One resolver over the whole scan serves every group: its indexes are
	// keyed by (group, port) and (group, name), so a group's lookups cannot
	// reach another group's listeners. That removes the per-group member-row
	// copies the old members map paid for on every tick.
	resolver := servicefacts.NewResolver(facts, pp)

	for _, cfg := range index.Configs() {
		// Named the way the resolver names the config's ports, so a stopped
		// worktree with a copy of the main checkout's file is `<repo>@<wt>`
		// here too, not a second group with the main checkout's name.
		name := pass.groupOf(cfg)
		g := table.get(name)
		path, dir := cfg.Path, cfg.Dir
		g.ConfigPath = &path
		g.RootDir = &dir
		if rank(state.SourceFile) > rank(g.Source) {
			g.Source = state.SourceFile
		}
		g.Services = serviceRowsWithFacts(cfg, name, resolver)
		g.Machine = machineRows(cfg, machinePorts, narrow)
	}

	out := make([]state.Group, 0, len(table.byName))
	for _, g := range table.byName {
		sort.Ints(g.Members)
		g.Status = status(*g)
		pass.describeCheckout(g)
		out = append(out, *g)
	}
	// Count and sum here so every caller of the shared join gets them. A caller
	// that fills a portless worker's sample afterwards runs it again; the
	// function is idempotent.
	FinalizeResources(out)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// StampServicePortRuns joins a Docker-published port to the managed run whose
// service row already claims it as PortActual. The listener belongs to
// docker-proxy, not to the run's process tree, so ancestry cannot attribute it;
// the service row can, because it was resolved from the run's port hint.
//
// The stamp matters beyond presentation: a group stop builds its targets from
// the snapshot, and without it the Compose CLI and the container are both
// targeted. Stopping the container makes the CLI exit before its own signal, so
// the stop reports an unconfirmed target and retains port reservations.
func StampServicePortRuns(pp []state.Port, groups []state.Group) {
	if len(pp) == 0 || len(groups) == 0 {
		return
	}
	type key struct {
		group string
		port  int
	}
	owners := make(map[key]state.Service, len(pp))
	for _, g := range groups {
		for _, svc := range g.Services {
			if svc.PortActual == nil || svc.RunID == nil || *svc.RunID == "" || svc.PID == nil {
				continue
			}
			owners[key{group: g.Name, port: *svc.PortActual}] = svc
		}
	}
	for i := range pp {
		if pp[i].Run != nil || pp[i].Type != state.TypeDocker || pp[i].Group == nil {
			continue
		}
		svc, ok := owners[key{group: *pp[i].Group, port: pp[i].Port}]
		if !ok {
			continue
		}
		pp[i].Run = &state.Run{
			ID: *svc.RunID, Group: *pp[i].Group, Name: svc.Name, RootPID: *svc.PID,
		}
	}
}

// FinalizeResources computes the worktree-level counts and sums from the
// service rows. It is separate from GroupsWith because a portless worker's
// sample is filled after the pure join; the caller runs this again once every
// service sample is in place. It is idempotent.
func FinalizeResources(groups []state.Group) {
	for i := range groups {
		groups[i].RunningServices = groups[i].RunningServiceCount()
		groups[i].Resources = groups[i].ResourceTotals()
	}
}

// describeCheckout fills in which project a group is a checkout of, which
// linked worktree it is, and the branch checked out there. A group named
// `<repo>@<worktree>` for the linked worktree it lives in is that worktree of
// repo; every other group is its own project.
func (r *resolutionPass) describeCheckout(g *state.Group) {
	g.Repo, g.Worktree, g.Branch, g.Identity = g.Name, "", "", ""
	if g.RootDir == nil {
		return
	}
	co, inRepo := r.locate(*g.RootDir)
	if !inRepo {
		g.Identity = WorktreeToken(*g.RootDir)
		return
	}
	g.Identity = WorktreeToken(co.Root)
	g.Branch = r.branch(co)
	if !co.Linked() {
		return
	}
	if repo, found := strings.CutSuffix(g.Name, "@"+co.Worktree); found && repo != "" {
		g.Repo, g.Worktree = repo, co.Worktree
	}
}

// ServiceRow adapts one `oberth.yaml` service to the published contract row,
// without any knowledge of what is running. `groups.config.get` returns the
// file through it, so what the editor reads back is exactly what the resolver
// publishes.
func ServiceRow(s Service) state.Service {
	svc := state.Service{
		Name:          s.Name,
		Prepare:       s.Prepare,
		PrepareInputs: nonNilStrings(s.PrepareInputs),
		Cmd:           s.Cmd,
		Cwd:           s.Cwd,
		Sources:       cloneStrings(s.Sources),
		ReadyTimeout:  s.EffectiveReadyTimeout().String(),
		DependsOn:     nonNilStrings(s.DependsOn),
		PortAuto:      s.PortAuto,
		Health:        optional(s.Health),
		Description:   optional(s.Description),
		Icon:          optional(s.Icon),
		Color:         optional(s.Color),
	}
	if s.Port != 0 {
		port := s.Port
		svc.Port = &port
	}
	hash := ServiceSpecHash(s)
	svc.ManifestHash = &hash
	return svc
}

// nonNilStrings clones a list and turns nil into the contract's empty array.
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return slices.Clone(in)
}

// ServiceRows adapts a whole config's services list.
func ServiceRows(cfg *Config) []state.Service {
	out := make([]state.Service, 0, len(cfg.Services))
	for _, s := range cfg.Services {
		out = append(out, ServiceRow(s))
	}
	return out
}

// optional turns an empty metadata string into the contract's null.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}

// machineRows renders a config's `machine:` references against what is
// listening: whether the declared port is there, and the service-manager unit
// it resolved to. The rows looked up are the whole scan's, not the group's own
// members — a dependency belongs to the machine's tier, not to this project.
func machineRows(cfg *Config, ports map[int]struct{}, narrow bool) []state.MachineRef {
	out := make([]state.MachineRef, 0, len(cfg.Machine))
	for _, m := range cfg.Machine {
		_, listening := ports[m.Port]
		out = append(out, state.MachineRef{
			Name: m.Name, Port: m.Port, Listening: listening,
			// Positive evidence stays a fact under a narrow table. Only
			// absence needs the table's coverage to decide whether it is
			// false or unknown.
			ListeningKnown: listening || !narrow,
		})
	}
	return out
}

// status is running when everything the group declares is up, stopped when
// nothing is, and partial in between. A group with no services is running as
// long as it has a listening port.
func status(g state.Group) string {
	declared := len(g.Services)
	if declared == 0 {
		if len(g.Members) > 0 {
			return "running"
		}
		return "stopped"
	}
	switch running := g.RunningServiceCount(); {
	case running == declared:
		return "running"
	case running > 0:
		return "partial"
	case len(g.Members) > 0:
		return "partial"
	default:
		return "stopped"
	}
}

// rank orders group sources by precedence so a group takes the strongest source
// any of its members resolved with. A pure function rather than a table: there
// is no state here to mutate.
func rank(s state.GroupSource) int {
	if s == state.SourceStart {
		return 2
	}
	if s == state.SourceFile {
		return 1
	}
	return 0
}
