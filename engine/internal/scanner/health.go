package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/spawn"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// maxHealthProbes bounds how many configured health checks run at once, the
// same ceiling the opt-in probe uses.
const maxHealthProbes = ports.MaxProbes

// Probe is one health check. Options.Probe replaces it for tests; production
// is ports.ProbeHealth.
type Probe func(host string, port int, path string, timeout time.Duration) ports.HealthResult

// ProbeContext is a synchronous, context-aware health probe. It must return
// when ctx is cancelled; the scanner always joins it rather than detaching it.
type ProbeContext func(ctx context.Context, host string, port int, path string, timeout time.Duration) ports.HealthResult

// ProbeConfigured applies the manifest health contract to an already
// attributed snapshot. The daemon uses the same operation through its scan
// loop; the direct CLI path uses it so `status` has the same service-level
// readiness fact when no daemon is available.
func ProbeConfigured(rows []state.Port, gg []state.Group, probe Probe, budget time.Duration) {
	probeConfigured(rows, gg, probe, budget)
}

// probeConfigured fills Port.health for every `oberth.yaml` service that
// declares a `health:` check and whose port is listening, and
// Service.health_status for every command check.
//
// Unlike the opt-in `include: ["health"]` probe, this one runs on every tick
// and for every subscriber: a health check in a config is a statement about
// what the service *is*, not a statistic a client may or may not want, so the
// daemon polls it as part of state (step 1A.7). HTTP and TCP share the port
// probe; a command check runs in the service's cwd. All kinds use
// HealthTimeout, ten in flight, and one round budget.
func probeConfigured(rows []state.Port, gg []state.Group, probe Probe, budget time.Duration) {
	var contextual ProbeContext
	if probe != nil {
		// Compatibility callbacks remain synchronous and must return. Do not
		// put them in detached goroutines to simulate cancellable I/O.
		contextual = func(_ context.Context, host string, port int, path string, timeout time.Duration) ports.HealthResult {
			return probe(host, port, path, timeout)
		}
	}
	_ = ProbeConfiguredContext(context.Background(), rows, gg, contextual, budget)
}

// ProbeConfiguredContext uses the same targets, join and budget as the legacy
// operation. Results are staged until every admitted probe has returned. A
// cancellation observed at that boundary changes neither port nor service rows.
// Once application starts, completion wins; owners of a larger transaction must
// still check their context before publishing.
func ProbeConfiguredContext(ctx context.Context, rows []state.Port, gg []state.Group, probe ProbeContext, budget time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if probe == nil {
		probe = ports.ProbeHealthContext
	}
	targets := healthTargets(rows, gg)
	if len(targets) == 0 {
		syncConfiguredHealth(rows, gg)
		return nil
	}

	// The whole round is bounded, not just one probe. This runs inside the
	// scan, which every write handler's republish and every kill's rescan is
	// queued behind (contract §38), so a project that declares health checks on
	// a dozen services — half of them accepting and never answering — must not
	// be able to add waves of HealthTimeout to somebody's "Save" button.
	var deadline time.Time
	if budget > 0 {
		deadline = time.Now().Add(budget)
	}

	results := make([]state.Health, len(targets))
	probed := make([]bool, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxHealthProbes)
admit:
	for i := range targets {
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break admit
		}
		if _, ok := ports.ProbeBudget(HealthTimeout, deadline); !ok || ctx.Err() != nil {
			<-sem
			break
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			timeout, ok := ports.ProbeBudget(HealthTimeout, deadline)
			if !ok {
				return
			}
			t := targets[i]
			if ctx.Err() != nil {
				return
			}
			var r ports.HealthResult
			switch t.kind {
			case groups.HealthTCP:
				r = ports.ProbeTCPContext(ctx, t.host, t.port, timeout)
			case groups.HealthCommand:
				r = ports.ProbeCommandContext(ctx, t.argv, t.dir, t.env, timeout)
			default:
				r = probe(ctx, t.host, t.port, t.path, timeout)
			}
			if ctx.Err() != nil {
				return
			}
			status, reason := state.NormalizeHealth(r.Status)
			observedAt := r.ObservedAt
			if observedAt.IsZero() {
				observedAt = time.Now().UTC()
			}
			results[i] = state.Health{
				Status:     status,
				Code:       r.StatusCode,
				LatencyMs:  r.Latency.Milliseconds(),
				Reason:     reason,
				ObservedAt: observedAt.Format(time.RFC3339Nano),
				Configured: true,
			}
			probed[i] = true
		}(i)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}

	// Port-backed results go onto the rows first; the join below copies them
	// onto the service rows that own those listeners.
	for i, t := range targets {
		if !probed[i] || t.kind == groups.HealthCommand {
			continue
		}
		h := results[i]
		for _, idx := range t.rows {
			row := h
			rows[idx].Health = &row
		}
	}
	syncConfiguredHealth(rows, gg)
	// Command checks have no listener to join through: write their verdict
	// straight onto the service row that declared them.
	for i, t := range targets {
		if !probed[i] || t.kind != groups.HealthCommand {
			continue
		}
		if t.group < 0 || t.service < 0 || t.group >= len(gg) || t.service >= len(gg[t.group].Services) {
			continue
		}
		h := results[i]
		gg[t.group].Services[t.service].HealthStatus = &h
	}
	return nil
}

// syncConfiguredHealth copies the configured health verdict from the port
// rows onto the service rows that declare the same listening port. The group
// resolver runs before probes, so doing this join only in groups.services
// would always publish the previous (usually nil) verdict. Keeping the join
// at commit time also covers a carried result when the health budget skips a
// round.
//
// A command check has no port join: its verdict is set directly by
// ProbeConfiguredContext. A stopped command-checked service keeps no verdict,
// because running the command would describe the machine, not the service.
func syncConfiguredHealth(rows []state.Port, gg []state.Group) {
	byPort := make(map[int]*state.Health)
	for i := range rows {
		h := rows[i].Health
		if h == nil || !h.Configured {
			continue
		}
		copy := *h
		byPort[rows[i].Port] = &copy
	}
	for gi := range gg {
		for si := range gg[gi].Services {
			svc := &gg[gi].Services[si]
			if svc.Health != nil && groups.ParseHealth(*svc.Health).Kind == groups.HealthCommand {
				if !svc.Running {
					svc.HealthStatus = nil
				}
				continue
			}
			svc.HealthStatus = nil
			if svc.PortActual == nil {
				continue
			}
			if h, ok := byPort[*svc.PortActual]; ok {
				copy := *h
				svc.HealthStatus = &copy
			}
		}
	}
}

// healthTarget is one configured probe: what to run, and where its result goes.
// Port-backed checks (HTTP, TCP) write to the port rows they joined; a command
// check writes to one service row.
type healthTarget struct {
	kind    groups.HealthKind
	port    int
	host    string
	path    string
	argv    []string
	dir     string
	env     []string
	rows    []int
	group   int
	service int
}

// healthTargets joins the configured health declarations against what is
// actually running. A port-backed check whose port is down has no row to carry
// a result, so it is not probed; the group already reports the service as not
// running. Command checks are built for running services, in the service's cwd,
// with ${port}/${<service>.port} expanded the same way cmd and prepare are.
func healthTargets(rows []state.Port, gg []state.Group) []healthTarget {
	type portCheck struct {
		kind groups.HealthKind
		path string
	}
	checks := map[int]portCheck{}
	var out []healthTarget

	for gi := range gg {
		portMap := groupPortMap(gg[gi])
		for si := range gg[gi].Services {
			svc := &gg[gi].Services[si]
			if svc.Health == nil || *svc.Health == "" {
				continue
			}
			h := groups.ParseHealth(*svc.Health)
			if h.Kind == groups.HealthCommand {
				// A command check runs in the service's cwd. Without the
				// project root there is no cwd to run it in, and the daemon's
				// own directory is not the service's: report no verdict rather
				// than a wrong one.
				if !svc.Running || gg[gi].RootDir == nil {
					continue
				}
				// Resolving the token shells out to git; only a check that
				// names ${worktree} pays for it.
				worktree := ""
				if strings.Contains(h.Command, "${worktree}") {
					worktree = groups.WorktreeTokenFor(*gg[gi].RootDir)
				}
				expanded := groups.Expand(h.Command, groups.Values{
					Self: svc.Name, Worktree: worktree, Ports: portMap,
				})
				out = append(out, healthTarget{
					kind: groups.HealthCommand, argv: spawn.SplitCmd(expanded),
					dir: filepath.Join(*gg[gi].RootDir, filepath.FromSlash(svc.Cwd)),
					env: commandEnv(svc.PortActual), group: gi, service: si,
				})
				continue
			}
			if svc.PortActual == nil {
				continue
			}
			if h.Kind == groups.HealthTCP {
				if _, taken := checks[h.Port]; !taken {
					checks[h.Port] = portCheck{kind: groups.HealthTCP}
				}
				continue
			}
			if _, taken := checks[*svc.PortActual]; !taken {
				checks[*svc.PortActual] = portCheck{kind: groups.HealthHTTP, path: h.Path}
			}
		}
	}

	byPort := map[int]*healthTarget{}
	var order []int
	for i := range rows {
		check, want := checks[rows[i].Port]
		if !want {
			continue
		}
		t, seen := byPort[rows[i].Port]
		if !seen {
			t = &healthTarget{kind: check.kind, port: rows[i].Port, path: check.path, group: -1, service: -1}
			byPort[rows[i].Port] = t
			order = append(order, rows[i].Port)
		}
		t.rows = append(t.rows, i)
		t.host = preferHost(t.host, rows[i])
	}
	for _, port := range order {
		out = append(out, *byPort[port])
	}
	return out
}

// groupPortMap is the service-name-to-port map ${port} and ${<service>.port}
// expand against. A service that is not running contributes its declared port;
// a port: auto service contributes the port it actually got.
func groupPortMap(g state.Group) map[string]int {
	ports := make(map[string]int, len(g.Services))
	for _, svc := range g.Services {
		switch {
		case svc.PortActual != nil:
			ports[svc.Name] = *svc.PortActual
		case svc.Port != nil:
			ports[svc.Name] = *svc.Port
		}
	}
	return ports
}

// commandEnv is the minimal environment a health command gets: the daemon's
// own plus the service's port, so a command may read either PORT or use the
// ${port} reference expanded into its arguments.
func commandEnv(port *int) []string {
	env := os.Environ()
	if port == nil {
		return env
	}
	v := strconv.Itoa(*port)
	return append(env, "PORT="+v, "BERTH_PORT="+v)
}

// preferHost picks the loopback address to probe. A service bound on IPv4 (or
// on every address) is reached at 127.0.0.1; one that only ever bound IPv6 is
// reached at [::1]. Picking per port rather than per row is what keeps a
// dual-stack listener from being probed twice with two different answers.
func preferHost(current string, p state.Port) string {
	if current == "127.0.0.1" {
		return current
	}
	switch p.BindAddress {
	case "::", "::1", "[::]", "[::1]":
		if current == "" {
			return "[::1]"
		}
		return current
	default:
		return "127.0.0.1"
	}
}
