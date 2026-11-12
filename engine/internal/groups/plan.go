package groups

import (
	"fmt"
	"maps"
	"slices"
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
	Path  string   // the manifest that was searched
	Name  string   // the `only` entry that is missing
	Known []string // the services that file does declare
}

func (e *UnknownServiceError) Error() string {
	message := fmt.Sprintf("no service %q in %s", e.Name, e.Path)
	if len(e.Known) == 0 {
		return message
	}
	return message + " (it declares " + strings.Join(e.Known, ", ") + ")"
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
// Plan keeps `only` in its original sense and does not pull dependencies in;
// PlanWith is the variant that does.
//
// Load has already rejected cycles and unknown depends_on, so the sort below
// cannot loop. A hand-built config that still contains one falls back to file
// order instead of dropping services.
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
	byName, order := serviceOrder(cfg)
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

	steps := make([]Step, 0, len(keep))
	for _, s := range topoSort(cfg, order) {
		if !keep[s.Name] {
			continue
		}
		step := Step{Service: s, IncludedBy: sortedKeys(includedBy[s.Name])}
		for _, dep := range s.DependsOn {
			if d, ok := byName[dep]; ok {
				step.Waits = append(step.Waits, d)
			}
		}
		steps = append(steps, step)
	}
	return steps, nil
}

// serviceOrder indexes the file's services by name and by position.
func serviceOrder(cfg *Config) (map[string]Service, map[string]int) {
	byName := make(map[string]Service, len(cfg.Services))
	order := make(map[string]int, len(cfg.Services))
	for i := range cfg.Services {
		s := cfg.Services[i]
		byName[s.Name], order[s.Name] = s, i
	}
	return byName, order
}

// closure adds every depends_on descendant of a root to keep and records which
// explicit roots reached it. One visited set per root makes a hand-built config
// with a cycle terminate; Load rejects those configs long before this runs.
func closure(roots []string, byName map[string]Service, keep map[string]bool,
	includedBy map[string]map[string]bool) {
	explicit := make(map[string]bool, len(roots))
	for _, root := range roots {
		explicit[root] = true
	}
	for _, root := range roots {
		visited := map[string]bool{root: true}
		pending := []string{root}
		for len(pending) > 0 {
			name := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			svc, ok := byName[name]
			if !ok {
				continue
			}
			for _, dep := range svc.DependsOn {
				dep = strings.TrimSpace(dep)
				if _, ok := byName[dep]; !ok || visited[dep] {
					continue
				}
				visited[dep] = true
				if !explicit[dep] {
					keep[dep] = true
					if includedBy[dep] == nil {
						includedBy[dep] = map[string]bool{}
					}
					includedBy[dep][root] = true
				}
				pending = append(pending, dep)
			}
		}
	}
}

func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	return slices.Sorted(maps.Keys(set))
}

// selection is the set of services the plan keeps.
func selection(cfg *Config, byName map[string]Service, only []string) (map[string]bool, error) {
	if len(only) == 0 {
		keep := make(map[string]bool, len(cfg.Services))
		for _, s := range cfg.Services {
			keep[s.Name] = true
		}
		return keep, nil
	}
	keep := make(map[string]bool, len(only))
	for _, raw := range only {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if _, declared := byName[name]; !declared {
			return nil, &UnknownServiceError{Path: cfg.Path, Name: name, Known: names(cfg)}
		}
		keep[name] = true
	}
	return keep, nil
}

// topoSort returns the services in dependency order, breaking ties by the order
// the file lists them. It is an explicit-stack post-order walk: a service is
// emitted only after everything it depends on.
func topoSort(cfg *Config, order map[string]int) []Service {
	byName := make(map[string]Service, len(cfg.Services))
	for _, s := range cfg.Services {
		byName[s.Name] = s
	}

	const (
		unvisited = 0
		onStack   = 1
		emitted   = 2
	)
	state := make(map[string]int, len(byName))
	out := make([]Service, 0, len(cfg.Services))

	type frame struct {
		service Service
		deps    []string
		next    int
	}
	for _, root := range cfg.Services {
		if state[root.Name] != unvisited {
			continue
		}
		state[root.Name] = onStack
		stack := []frame{{service: root, deps: orderedDeps(root, order)}}
		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			if top.next < len(top.deps) {
				name := top.deps[top.next]
				top.next++
				dep, ok := byName[name]
				// An edge back into the current branch, or into an already
				// emitted service: Load would have rejected a cycle, so there
				// is nothing left to recurse into.
				if !ok || state[name] != unvisited {
					continue
				}
				state[name] = onStack
				stack = append(stack, frame{service: dep, deps: orderedDeps(dep, order)})
				continue
			}
			state[top.service.Name] = emitted
			out = append(out, top.service)
			stack = stack[:len(stack)-1]
		}
	}
	return out
}

// orderedDeps lists a service's dependencies in the order the file declares
// the services.
func orderedDeps(s Service, order map[string]int) []string {
	deps := slices.Clone(s.DependsOn)
	slices.SortStableFunc(deps, func(a, b string) int { return order[a] - order[b] })
	return deps
}

// names lists a config's service names in file order.
func names(cfg *Config) []string {
	out := make([]string, len(cfg.Services))
	for i := range cfg.Services {
		out[i] = cfg.Services[i].Name
	}
	return out
}
