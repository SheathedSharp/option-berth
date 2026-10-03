package scanner

import (
	"context"
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
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
// declares a `health:` path and whose port is listening.
//
// Unlike the opt-in `include: ["health"]` probe, this one runs on every tick
// and for every subscriber: a health path in a config is a statement about what
// the service *is*, not a statistic a client may or may not want, so the
// daemon polls it as part of state (step 1A.7). Everything else about it is the
// same probe — one HTTP GET, HealthTimeout, ten in flight.
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
	// queued behind (contract §38), so a project that declares health paths on
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
			r := probe(ctx, t.host, t.port, t.path, timeout)
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

	for i, t := range targets {
		if !probed[i] {
			// Left for carryHealth: the previous tick's verdict beats a
			// flicker to "unknown" for a probe that never ran.
			continue
		}
		h := results[i]
		for _, idx := range t.rows {
			row := h
			rows[idx].Health = &row
		}
	}
	syncConfiguredHealth(rows, gg)
	return nil
}

// syncConfiguredHealth copies the configured health verdict from the port
// rows onto the service rows that declare the same listening port. The group
// resolver runs before probes, so doing this join only in groups.services
// would always publish the previous (usually nil) verdict. Keeping the join
// at commit time also covers a carried result when the health budget skips a
// round.
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

// healthTarget is one configured probe: the port, the path from the config and
// the rows the result is written back to.
type healthTarget struct {
	port int
	host string
	path string
	rows []int
}

// healthTargets joins the configured health paths against the ports actually
// listening. A service whose port is down has no row to carry a result, so it
// simply is not probed; the group already reports it as not running.
func healthTargets(rows []state.Port, gg []state.Group) []healthTarget {
	paths := map[int]string{}
	for _, g := range gg {
		for _, svc := range g.Services {
			if svc.Health == nil || *svc.Health == "" || svc.PortActual == nil {
				continue
			}
			if _, taken := paths[*svc.PortActual]; !taken {
				paths[*svc.PortActual] = *svc.Health
			}
		}
	}
	if len(paths) == 0 {
		return nil
	}

	byPort := map[int]*healthTarget{}
	out := []healthTarget{}
	order := []int{}
	for i := range rows {
		path, want := paths[rows[i].Port]
		if !want {
			continue
		}
		t, seen := byPort[rows[i].Port]
		if !seen {
			t = &healthTarget{port: rows[i].Port, path: path}
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
