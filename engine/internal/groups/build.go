package groups

import (
	"sort"
	"strings"

	"github.com/sheathedsharp/option-berth/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// Groups builds the group collection from resolved ports. Every group that at
// least one port resolves to appears, plus every valid `oberth.yaml` the index
// knows about — a project whose services are all down is still a group, it is
// just stopped.
//
// index may be nil, in which case groups carry no services and no config path.
func Groups(pp []state.Port, index *Index) []state.Group { return GroupsWith(pp, index, nil) }

// Liveness is the control path's readiness check for a portless dependency.
// State publication instead consumes one servicefacts.Provider snapshot.
type Liveness interface {
	Live(group, service string) bool
}

// GroupsWith captures runtime facts once when reg implements
// servicefacts.Provider. Otherwise only observed listeners are available.
func GroupsWith(pp []state.Port, index *Index, reg Registry) []state.Group {
	if index == nil {
		index = NewIndex()
	}
	pass := newResolutionPass(index)
	var facts servicefacts.Snapshot
	if provider, ok := reg.(servicefacts.Provider); ok {
		facts = provider.ServiceFacts()
	}
	byName := map[string]*state.Group{}
	memberPorts := map[string]map[int]struct{}{}
	machinePorts := make(map[int]struct{}, len(pp))
	for _, p := range pp {
		machinePorts[p.Port] = struct{}{}
	}

	order := func(name string) *state.Group {
		g, ok := byName[name]
		if !ok {
			g = &state.Group{Name: name, Source: state.SourceAuto, Members: []int{}, Services: []state.Service{}, Machine: []state.MachineRef{}}
			byName[name] = g
		}
		return g
	}

	for _, p := range pp {
		if p.Group == nil || *p.Group == "" {
			continue
		}
		g := order(*p.Group)
		if p.GroupSource != nil && rank(*p.GroupSource) > rank(g.Source) {
			g.Source = *p.GroupSource
		}
		seen := memberPorts[*p.Group]
		if seen == nil {
			seen = map[int]struct{}{}
			memberPorts[*p.Group] = seen
		}
		if _, exists := seen[p.Port]; !exists {
			seen[p.Port] = struct{}{}
			g.Members = append(g.Members, p.Port)
		}
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
		g := order(name)
		path, dir := cfg.Path, cfg.Dir
		g.ConfigPath = &path
		g.RootDir = &dir
		if rank(state.SourceFile) > rank(g.Source) {
			g.Source = state.SourceFile
		}
		g.Services = serviceRowsWithFacts(cfg, name, resolver)
		g.Machine = machineRows(cfg, machinePorts)
	}

	out := make([]state.Group, 0, len(byName))
	for _, g := range byName {
		sort.Ints(g.Members)
		g.Status = status(*g)
		pass.describeCheckout(g)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// describeCheckout fills in which project a group is a checkout of, which
// linked worktree it is, and the branch checked out there. A group named
// `<repo>@<worktree>` for the linked worktree it lives in is that worktree of
// repo; every other group is its own project.
func (r *resolutionPass) describeCheckout(g *state.Group) {
	g.Repo, g.Worktree, g.Branch = g.Name, "", ""
	if g.RootDir == nil {
		return
	}
	co, ok := r.locate(*g.RootDir)
	if !ok {
		return
	}
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
		Name:      s.Name,
		Prepare:   s.Prepare,
		Cmd:       s.Cmd,
		Cwd:       s.Cwd,
		DependsOn: append([]string{}, s.DependsOn...),
	}
	if svc.DependsOn == nil {
		svc.DependsOn = []string{}
	}
	if s.Port != 0 {
		port := s.Port
		svc.Port = &port
	}
	svc.PortAuto = s.PortAuto
	svc.Health = optional(s.Health)
	hash := ServiceSpecHash(s)
	svc.ManifestHash = &hash
	svc.Description = optional(s.Description)
	svc.Icon = optional(s.Icon)
	svc.Color = optional(s.Color)
	return svc
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
func machineRows(cfg *Config, ports map[int]struct{}) []state.MachineRef {
	out := make([]state.MachineRef, 0, len(cfg.Machine))
	for _, m := range cfg.Machine {
		ref := state.MachineRef{Name: m.Name, Port: m.Port}
		_, ref.Listening = ports[m.Port]
		out = append(out, ref)
	}
	return out
}

// status is running when everything the group declares is up, stopped when
// nothing is, and partial in between. A group with no services is running as
// long as it has a listening port.
func status(g state.Group) string {
	if len(g.Services) == 0 {
		if len(g.Members) > 0 {
			return "running"
		}
		return "stopped"
	}
	up := 0
	for _, s := range g.Services {
		if s.Running {
			up++
		}
	}
	switch {
	case up == len(g.Services):
		return "running"
	case up == 0:
		if len(g.Members) > 0 {
			return "partial"
		}
		return "stopped"
	default:
		return "partial"
	}
}

// rank orders group sources by precedence so a group takes the strongest
// source any of its members resolved with.
func rank(s state.GroupSource) int {
	switch s {
	case state.SourceStart:
		return 2
	case state.SourceFile:
		return 1
	default:
		return 0
	}
}
