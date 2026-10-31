package cmd

import (
	"fmt"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// scopeStatusDocument applies `status --only`. It runs after the document and
// its mark were built from the full worktree, so a scoped read never writes a
// partial baseline for the next comparison.
//
// The selection is the named services plus the transitive `depends_on`
// closure: a task that needs `api` also needs what `api` waits for. Ports,
// exits and the service half of `changed` follow that selection. `machine:` is
// left whole — it is a project-level dependency list, and the manifest does not
// record which service references which entry.
func scopeStatusDocument(doc *statusDocument, names []string) error {
	if len(names) == 0 {
		return nil
	}
	selected, err := statusOnlySelection(doc.Worktree.Services, names)
	if err != nil {
		return err
	}

	services := make([]state.Service, 0, len(selected))
	for _, svc := range doc.Worktree.Services {
		if selected[svc.Name] {
			services = append(services, svc)
		}
	}
	doc.Worktree.Services = services

	ports := make(map[int]bool, len(services)*2)
	for _, svc := range services {
		if svc.Port != nil {
			ports[*svc.Port] = true
		}
		if svc.PortActual != nil {
			ports[*svc.PortActual] = true
		}
	}
	listeners := make([]state.Port, 0, len(doc.Ports))
	for _, port := range doc.Ports {
		if ports[port.Port] {
			listeners = append(listeners, port)
		}
	}
	doc.Ports = listeners

	exits := make([]rpc.RunRecord, 0, len(doc.Exits))
	for _, run := range doc.Exits {
		if selected[run.Name] {
			exits = append(exits, run)
		}
	}
	doc.Exits = exits

	scopeStatusChange(doc.Changed, selected, ports)
	return nil
}

// statusOnlySelection resolves --only names to services and their dependency
// closure. An unknown name is not silently ignored: it is usually a typo, and
// a scoped read that answers about a service the project does not declare
// would look like a clean result.
func statusOnlySelection(services []state.Service, names []string) (map[string]bool, error) {
	declared := make(map[string]state.Service, len(services))
	for _, svc := range services {
		declared[svc.Name] = svc
	}

	wanted := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, usageError{fmt.Errorf("--only contains an empty service name")}
		}
		if !seen[name] {
			seen[name] = true
			wanted = append(wanted, name)
		}
	}

	unknown := make([]string, 0, len(wanted))
	for _, name := range wanted {
		if _, ok := declared[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		hint := "this project declares no services"
		if len(services) > 0 {
			declaredNames := make([]string, 0, len(services))
			for _, svc := range services {
				declaredNames = append(declaredNames, svc.Name)
			}
			hint = "declared services: " + strings.Join(declaredNames, ", ")
		}
		return nil, failHint("not_found", "no service named "+strings.Join(unknown, ", "), hint)
	}

	selected := make(map[string]bool, len(declared))
	queue := append([]string(nil), wanted...)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if selected[name] {
			continue
		}
		selected[name] = true
		for _, dep := range declared[name].DependsOn {
			if _, ok := declared[dep]; ok && !selected[dep] {
				queue = append(queue, dep)
			}
		}
	}
	return selected, nil
}

// scopeStatusChange drops changes outside the selected services and their
// ports. File and HEAD changes have no service ownership in this document, so
// they stay: `--only` narrows runtime facts, not the repository summary.
func scopeStatusChange(change *statusChange, services map[string]bool, ports map[int]bool) {
	if change == nil {
		return
	}
	change.ServicesStarted = onlyNamed(change.ServicesStarted, services)
	change.ServicesStopped = onlyNamed(change.ServicesStopped, services)
	change.PortsOpened = onlyPorts(change.PortsOpened, ports)
	change.PortsClosed = onlyPorts(change.PortsClosed, ports)
	change.Empty = change.isEmpty()
}

func onlyNamed(names []string, keep map[string]bool) []string {
	if len(names) == 0 {
		return names
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if keep[name] {
			out = append(out, name)
		}
	}
	return out
}

func onlyPorts(ports []int, keep map[int]bool) []int {
	if len(ports) == 0 {
		return ports
	}
	out := make([]int, 0, len(ports))
	for _, port := range ports {
		if keep[port] {
			out = append(out, port)
		}
	}
	return out
}
