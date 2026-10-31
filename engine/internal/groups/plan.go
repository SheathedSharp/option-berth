package groups

import (
	"fmt"
	"sort"
	"strings"
)

// Step is one service in a start plan, with the dependencies `groups.start`
// has to see ready before it spawns this one. A ported dependency is ready when
// its listener is present; a no-port dependency is ready when its run is live.
//
// IncludedBy names the explicitly selected services whose depends_on closure
// brought this service into the plan, sorted. It is empty for a service the
// caller named (or for a plan that names nothing and therefore selects every
// service), so a non-empty IncludedBy is exactly "this one is here because
// something you named requires it".
type Step struct {
	Service    Service
	Waits      []Service
	IncludedBy []string
}

// PlanOptions narrows a start plan. The zero value keeps Plan's original
// meaning: no filter, so every service is explicit.
type PlanOptions struct {
	// Only names the services the caller asked for. Empty means every service.
	Only []string
	// IncludeDependencies adds the transitive depends_on closure of Only to the
	// plan. With it false the named services are kept as named — an older
	// daemon's behaviour, which `oberth restart --only` still relies on.
	IncludeDependencies bool
}

// UnknownServiceError names an `only` entry that is not in the file. It maps
// onto the contract's `not_found`.
type UnknownServiceError struct {
	Path  string
	Name  string
	Known []string
}

func (e *UnknownServiceError) Error() string {
	if len(e.Known) == 0 {
		return fmt.Sprintf("no service %q in %s", e.Name, e.Path)
	}
	return fmt.Sprintf("no service %q in %s (it declares %s)",
		e.Name, e.Path, strings.Join(e.Known, ", "))
}

// Plan orders a config's services for `oberth up`: dependencies first, then
// everything that depends on them, with services at the same depth kept in the
// order the file lists them so a plan reads like the file.
//
// only restricts the plan to the named services; an unknown name is an error
// rather than a silent no-op, because "oberth up --only ap" should say so. A
// service kept by `only` still waits for the dependencies it declares — they
// may already be running, which is the common case, and `groups.start` skips
// what is up.
//
// Plan keeps the original meaning of `only`: it does not pull dependencies in.
// Use PlanWith to include them.
//
// Load has already rejected cycles and unknown depends_on, so the sort here
// cannot loop; a config built by hand that still contains one degrades to file
// order rather than dropping services.
func Plan(cfg *Config, only []string) ([]Step, error) {
	return PlanWith(cfg, PlanOptions{Only: only})
}

// PlanWith is Plan with dependency closure. When IncludeDependencies is set,
// every service reachable through depends_on from an explicitly named service
// joins the plan; a service both named and reached is still explicit and
// carries no IncludedBy. Unknown names in Only remain an error.
func PlanWith(cfg *Config, opts PlanOptions) ([]Step, error) {
	if cfg == nil {
		return nil, nil
	}
	byName := make(map[string]Service, len(cfg.Services))
	order := make(map[string]int, len(cfg.Services))
	for i, s := range cfg.Services {
		byName[s.Name] = s
		order[s.Name] = i
	}

	keep, err := selection(cfg, byName, opts.Only)
	if err != nil {
		return nil, err
	}
	includedBy := map[string]map[string]bool{}
	if opts.IncludeDependencies {
		// The names the caller gave are the roots; snapshot them before the
		// closure adds services to keep, so a reached service never becomes a
		// root of its own.
		closure(sortedKeys(keep), byName, keep, includedBy)
	}

	sorted := topoSort(cfg, order)

	steps := make([]Step, 0, len(keep))
	for _, s := range sorted {
		if !keep[s.Name] {
			continue
		}
		step := Step{Service: s, IncludedBy: sortedKeys(includedBy[s.Name])}
		for _, dep := range s.DependsOn {
			d, ok := byName[dep]
			if !ok {
				continue
			}
			step.Waits = append(step.Waits, d)
		}
		steps = append(steps, step)
	}
	return steps, nil
}

// closure adds every depends_on descendant of a root to keep and records which
// explicit roots reached it. A visited set per root makes a hand-built config
// with a cycle terminate; Load rejects those configs long before this runs.
func closure(roots []string, byName map[string]Service, keep map[string]bool,
	includedBy map[string]map[string]bool) {
	explicit := make(map[string]bool, len(roots))
	for _, root := range roots {
		explicit[root] = true
	}
	for _, root := range roots {
		visited := map[string]bool{}
		var visit func(name string)
		visit = func(name string) {
			if visited[name] {
				return
			}
			visited[name] = true
			svc, ok := byName[name]
			if !ok {
				return
			}
			for _, dep := range svc.DependsOn {
				dep = strings.TrimSpace(dep)
				if _, ok := byName[dep]; !ok {
					continue
				}
				if !explicit[dep] {
					keep[dep] = true
					if includedBy[dep] == nil {
						includedBy[dep] = map[string]bool{}
					}
					includedBy[dep][root] = true
				}
				visit(dep)
			}
		}
		visit(root)
	}
}

func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// selection is the set of services the plan keeps.
func selection(cfg *Config, byName map[string]Service, only []string) (map[string]bool, error) {
	keep := make(map[string]bool, len(cfg.Services))
	if len(only) == 0 {
		for _, s := range cfg.Services {
			keep[s.Name] = true
		}
		return keep, nil
	}
	for _, name := range only {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := byName[name]; !ok {
			return nil, &UnknownServiceError{Path: cfg.Path, Name: name, Known: names(cfg)}
		}
		keep[name] = true
	}
	return keep, nil
}

// topoSort returns the services in dependency order, breaking ties by the
// order the file lists them.
func topoSort(cfg *Config, order map[string]int) []Service {
	state := map[string]int{} // 0 unvisited, 1 in progress, 2 done
	out := make([]Service, 0, len(cfg.Services))

	byName := make(map[string]Service, len(cfg.Services))
	for _, s := range cfg.Services {
		byName[s.Name] = s
	}

	var visit func(Service)
	visit = func(s Service) {
		switch state[s.Name] {
		case 1, 2:
			// Already emitted, or an edge back into the branch we are in: a
			// cycle Load would have rejected. Either way, do not recurse.
			return
		}
		state[s.Name] = 1
		deps := append([]string{}, s.DependsOn...)
		sort.SliceStable(deps, func(i, j int) bool { return order[deps[i]] < order[deps[j]] })
		for _, dep := range deps {
			if d, ok := byName[dep]; ok {
				visit(d)
			}
		}
		state[s.Name] = 2
		out = append(out, s)
	}

	for _, s := range cfg.Services {
		visit(s)
	}
	return out
}

// names lists a config's service names in file order.
func names(cfg *Config) []string {
	out := make([]string, 0, len(cfg.Services))
	for _, s := range cfg.Services {
		out = append(out, s.Name)
	}
	return out
}
