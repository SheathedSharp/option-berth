package cmd

import (
	"context"
	"fmt"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// startConfig loads the manifest `oberth start` resolved for this command. A
// missing or invalid manifest is not an error here: the command still runs,
// it simply has no address book.
func startConfig(path string) *groups.Config {
	if path == "" {
		return nil
	}
	cfg, err := groups.Load(path)
	if err != nil {
		return nil
	}
	return cfg
}

// expandStartAddresses replaces `${<service>.port}`, `${<service>.url}` and
// `${worktree}` in a one-off command with this worktree's addresses. It is what
// makes `oberth start -- curl '${api.url}/health'` reach the port belonging to
// *this* checkout even when the service uses `port: auto`.
//
// A recognized reference that cannot be resolved is an error, never a literal:
// curling `${api.url}` is a bug the caller should see, not a request to a host
// named after the manifest. Bare `${port}` and `${url}` are expanded when
// `--port` supplies their value; otherwise they are left alone, because a
// one-off command has no service of its own.
func expandStartAddresses(ctx context.Context, cfg *groups.Config, argv []string, portHint int) ([]string, error) {
	if cfg == nil {
		return argv, nil
	}
	names, hasWorktree, hasBare := startReferences(argv)
	if len(names) == 0 && !hasWorktree && !hasBare {
		return argv, nil
	}

	book := cfg.AddressBook(nil)
	// Runtime facts are only needed for an address the declarations do not
	// settle: a local `port: auto` service, or a workspace whose service is on
	// an assigned port. Asking the daemon (and scanning) when every reference is
	// already fixed would slow down commands that do not need it.
	if needsRuntime(cfg, book, names) {
		runtime, err := startRuntimeGroups(ctx)
		if err != nil {
			// Unknown is not "not running": without runtime facts an assigned
			// port cannot be resolved, and running the command with a literal
			// ${...} would be worse than refusing it.
			return nil, fmt.Errorf("cannot resolve this worktree's addresses: %w", err)
		}
		book = cfg.AddressBook(runtime)
	}

	for _, name := range names {
		if _, ok := book.Ports[name]; ok {
			continue
		}
		if reason, unresolved := book.Unresolved[name]; unresolved {
			return nil, fmt.Errorf("no address for %s: %s", name, reason)
		}
		return nil, fmt.Errorf("%s names no service, machine, or workspace reference in %s", name, cfg.Path)
	}
	if hasWorktree && book.Worktree == "" {
		return nil, fmt.Errorf("cannot resolve ${worktree}: %s is not in a checkout", cfg.Dir)
	}

	values := groups.Values{Worktree: book.Worktree, Ports: book.Ports}
	if hasBare && portHint > 0 {
		values.Ports[""] = portHint
	}
	out := append([]string(nil), argv...)
	for i := range out {
		out[i] = groups.Expand(out[i], values)
	}
	return out, nil
}

// startReferences lists the names a command's references use, whether it names
// ${worktree}, and whether it carries a bare ${port}/${url}. Bare references
// resolve to "" in groups.Refs, which is why they are tracked separately.
func startReferences(argv []string) (names []string, hasWorktree, hasBare bool) {
	seen := map[string]bool{}
	for _, arg := range argv {
		if groups.HasWorktreeRef(arg) {
			hasWorktree = true
		}
		for _, name := range groups.Refs(arg, "") {
			if name == "" {
				hasBare = true
				continue
			}
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	return names, hasWorktree, hasBare
}

// needsRuntime reports whether a named reference is both unresolved in the
// declaration-only book and capable of being resolved from runtime facts. An
// unknown name or a service with no port cannot be, so it does not justify a
// daemon read or a scan.
func needsRuntime(cfg *groups.Config, book groups.AddressBook, names []string) bool {
	for _, name := range names {
		if _, ok := book.Ports[name]; ok {
			continue
		}
		if cfg.RuntimeAddress(name) {
			return true
		}
	}
	return false
}

// startRuntimeGroups reads the same runtime facts status does: the daemon's
// snapshot when it is running, otherwise one direct scan. It never starts a
// daemon; `oberth start` registering the run may do that afterwards.
//
// The run registry is folded in because `up` returns as soon as a service has
// been spawned and survived the startup window, while the scanner publishes its
// listener a moment later. The port `groups.start` assigned is already a fact;
// without it an agent running `up` then `oberth start -- curl ${api.url}`
// would race the scan and be told its service is not running.
func startRuntimeGroups(ctx context.Context) ([]state.Group, error) {
	if c, err := dialDaemon(ctx); err == nil {
		defer c.Close()
		var snap state.Snapshot
		if err := c.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snap); err == nil {
			var runs rpc.RunsListResult
			if err := c.Call(ctx, "runs.list", rpc.Empty{}, &runs); err == nil {
				return overlayRunAddresses(snap.Groups, runs.Runs), nil
			}
			return snap.Groups, nil
		}
	}
	_, gg, err := scanGroups()
	return gg, err
}

// overlayRunAddresses adds the assigned port of a live run to its service row
// when the scanner has not published that listener yet. It matches by manifest
// path, so a run in another checkout can only enrich that checkout's group; it
// never guesses from a service name alone.
func overlayRunAddresses(groups []state.Group, runs []rpc.RunRecord) []state.Group {
	if len(runs) == 0 {
		return groups
	}
	byConfig := make(map[string]int, len(groups))
	for i := range groups {
		if groups[i].ConfigPath != nil {
			byConfig[canonicalPath(*groups[i].ConfigPath)] = i
		}
	}
	for _, run := range runs {
		if run.ConfigPath == "" || run.PortHint == nil || *run.PortHint <= 0 || run.Status == "exited" {
			continue
		}
		key := canonicalPath(run.ConfigPath)
		idx, ok := byConfig[key]
		if !ok {
			path := run.ConfigPath
			groups = append(groups, state.Group{ConfigPath: &path, Services: []state.Service{}, Machine: []state.MachineRef{}})
			idx = len(groups) - 1
			byConfig[key] = idx
		}
		port := *run.PortHint
		found := false
		for i := range groups[idx].Services {
			if groups[idx].Services[i].Name != run.Name {
				continue
			}
			found = true
			if groups[idx].Services[i].PortActual == nil {
				groups[idx].Services[i].PortActual = &port
				groups[idx].Services[i].Running = true
			}
			break
		}
		if !found {
			groups[idx].Services = append(groups[idx].Services,
				state.Service{Name: run.Name, Running: true, PortActual: &port})
		}
	}
	return groups
}

// canonicalPath uses the manifest layer's one notion of a path identity.
func canonicalPath(path string) string { return groups.Canonical(path) }
