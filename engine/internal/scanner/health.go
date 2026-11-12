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

// maxHealthProbes is how many configured health checks may be in flight at
// once — the ceiling the opt-in probe already uses.
const maxHealthProbes = ports.MaxProbes

// Probe is one health check. Tests replace it through Options.Probe;
// production uses ports.ProbeHealth.
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
// what the service is, not a statistic a client may or may not want. HTTP and
// TCP share the port probe; a command check runs in the service's cwd. All
// kinds use HealthTimeout, ten in flight, and one round budget.
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

	round, err := runHealthRound(ctx, targets, probe, budget)
	if err != nil {
		return err
	}
	applyHealthRound(rows, gg, targets, round)
	return nil
}

// healthRound is one round's staged output. Results are written by their own
// probe goroutine and read only after wg.Wait, so no lock is needed.
type healthRound struct {
	results []state.Health
	probed  []bool
}

// runHealthRound admits probes up to maxHealthProbes and the round's deadline,
// then joins every one it admitted.
//
// The budget is why this exists: the round runs inside the scan, which every
// write handler's republish and every kill's rescan queue behind, so a project
// that declares health checks on a dozen services — half of them accepting and
// never answering — must not be able to add waves of HealthTimeout to
// somebody's "Save" button.
func runHealthRound(ctx context.Context, targets []healthTarget, probe ProbeContext, budget time.Duration) (*healthRound, error) {
	var deadline time.Time
	if budget > 0 {
		deadline = time.Now().Add(budget)
	}

	round := &healthRound{
		results: make([]state.Health, len(targets)),
		probed:  make([]bool, len(targets)),
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, maxHealthProbes)

	for i := range targets {
		if !admitHealthProbe(ctx, slots, deadline) {
			break
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-slots }()
			recordHealthProbe(ctx, probe, targets[i], deadline, round, i)
		}(i)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return round, nil
}

// admitHealthProbe takes one probe slot while the round still owns time and
// the caller still owns the scan. A refused admission releases nothing the
// caller held: either the slot was never taken, or it is put back before the
// refusal. The caller stops admitting; probes already running still finish.
func admitHealthProbe(ctx context.Context, slots chan struct{}, deadline time.Time) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	if _, ok := ports.ProbeBudget(HealthTimeout, deadline); !ok || ctx.Err() != nil {
		<-slots
		return false
	}
	return true
}

// recordHealthProbe writes one verdict into the round. A probe that loses its
// budget or its caller leaves the slot unmarked, so the row keeps the carried
// result instead of a verdict that was never observed.
func recordHealthProbe(ctx context.Context, probe ProbeContext, target healthTarget, deadline time.Time, round *healthRound, i int) {
	timeout, ok := ports.ProbeBudget(HealthTimeout, deadline)
	if !ok || ctx.Err() != nil {
		return
	}
	verdict, ok := probeTarget(ctx, probe, target, timeout)
	if !ok {
		return
	}
	round.results[i] = verdict
	round.probed[i] = true
}

// probeTarget runs one target through the adapter its kind names. It reports
// false when the probe was interrupted: a cancelled probe is not a verdict, so
// its row keeps whatever it had rather than publishing an observation that
// never happened.
func probeTarget(ctx context.Context, probe ProbeContext, t healthTarget, timeout time.Duration) (state.Health, bool) {
	var result ports.HealthResult
	switch t.kind {
	case groups.HealthTCP:
		result = ports.ProbeTCPContext(ctx, t.host, t.port, timeout)
	case groups.HealthCommand:
		result = ports.ProbeCommandContext(ctx, t.argv, t.dir, t.env, timeout)
	default:
		result = probe(ctx, t.host, t.port, t.path, timeout)
	}
	if ctx.Err() != nil {
		return state.Health{}, false
	}
	status, reason := state.NormalizeHealth(result.Status)
	observedAt := result.ObservedAt
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	return state.Health{
		Status:     status,
		Code:       result.StatusCode,
		LatencyMs:  result.Latency.Milliseconds(),
		Reason:     reason,
		ObservedAt: observedAt.Format(time.RFC3339Nano),
		Configured: true,
	}, true
}

// applyHealthRound writes a completed round onto the snapshot it was computed
// for. Port-backed results go onto the rows first; the join then copies them
// onto the service rows that own those listeners. Command checks have no
// listener to join through, so their verdict goes straight onto the service
// row that declared them.
func applyHealthRound(rows []state.Port, gg []state.Group, targets []healthTarget, round *healthRound) {
	applyPortHealth(rows, targets, round)
	syncConfiguredHealth(rows, gg)
	applyCommandHealth(gg, targets, round)
}

// applyPortHealth writes listener-backed verdicts onto the port rows they
// joined. Command checks have no listener, so they are skipped here and
// written onto the service after the port join.
func applyPortHealth(rows []state.Port, targets []healthTarget, round *healthRound) {
	for i, t := range targets {
		if !round.probed[i] || t.kind == groups.HealthCommand {
			continue
		}
		for _, idx := range t.rows {
			verdict := round.results[i]
			rows[idx].Health = &verdict
		}
	}
}

// applyCommandHealth writes a command check onto the service that declared
// it. A target that no longer names a service row is dropped: the verdict
// would otherwise describe a service the snapshot does not contain.
func applyCommandHealth(gg []state.Group, targets []healthTarget, round *healthRound) {
	for i, t := range targets {
		if !round.probed[i] || t.kind != groups.HealthCommand {
			continue
		}
		if !commandTargetInRange(gg, t) {
			continue
		}
		verdict := round.results[i]
		gg[t.group].Services[t.service].HealthStatus = &verdict
	}
}

func commandTargetInRange(gg []state.Group, t healthTarget) bool {
	return t.group >= 0 && t.service >= 0 && t.group < len(gg) && t.service < len(gg[t.group].Services)
}

// syncConfiguredHealth copies the configured health verdict from the port
// rows onto the service rows that declare the same listening port. The group
// resolver runs before probes, so doing this join only in groups.services
// would always publish the previous (usually nil) verdict. Keeping the join at
// commit time also covers a carried result when the health budget skips a
// round.
//
// A command check has no port join: its verdict is set directly by
// ProbeConfiguredContext. A stopped command-checked service keeps no verdict,
// because running the command would describe the machine, not the service.
func syncConfiguredHealth(rows []state.Port, gg []state.Group) {
	byPort := configuredHealthByPort(rows)
	for gi := range gg {
		for si := range gg[gi].Services {
			joinServiceHealth(&gg[gi].Services[si], byPort)
		}
	}
}

// configuredHealthByPort indexes the configured verdicts a port row is
// carrying. An opt-in probe that is not part of the declaration is not a
// service's health.
func configuredHealthByPort(rows []state.Port) map[int]*state.Health {
	byPort := make(map[int]*state.Health)
	for i := range rows {
		h := rows[i].Health
		if h == nil || !h.Configured {
			continue
		}
		copy := *h
		byPort[rows[i].Port] = &copy
	}
	return byPort
}

// joinServiceHealth copies a listener's configured verdict onto the service
// that owns that listener. A command check has no port to join through: a
// stopped service drops the verdict, because running the command would
// describe the machine, and a running one keeps the verdict the probe wrote.
func joinServiceHealth(svc *state.Service, byPort map[int]*state.Health) {
	if svc.Health != nil && groups.ParseHealth(*svc.Health).Kind == groups.HealthCommand {
		if !svc.Running {
			svc.HealthStatus = nil
		}
		return
	}
	svc.HealthStatus = nil
	if svc.PortActual == nil {
		return
	}
	if h, ok := byPort[*svc.PortActual]; ok {
		copy := *h
		svc.HealthStatus = &copy
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
type portCheck struct {
	kind groups.HealthKind
	path string
}

func healthTargets(rows []state.Port, gg []state.Group) []healthTarget {
	checks := make(map[int]portCheck)
	var out []healthTarget
	for gi := range gg {
		out = append(out, serviceHealthTargets(gg[gi], gi, checks)...)
	}
	return append(out, listeningHealthTargets(rows, checks)...)
}

// serviceHealthTargets turns one group's declarations into command probes and
// notes the port checks that still have to be joined to a live listener.
func serviceHealthTargets(group state.Group, gi int, checks map[int]portCheck) []healthTarget {
	portMap := groupPortMap(group)
	var out []healthTarget
	for si := range group.Services {
		svc := &group.Services[si]
		if svc.Health == nil || *svc.Health == "" {
			continue
		}
		h := groups.ParseHealth(*svc.Health)
		if h.Kind == groups.HealthCommand {
			if target, ok := commandHealthTarget(group, svc, h, portMap, gi, si); ok {
				out = append(out, target)
			}
			continue
		}
		notePortCheck(checks, svc, h)
	}
	return out
}

// commandHealthTarget builds one command probe. Without the project root
// there is no cwd to run it in, and the daemon's own directory is not the
// service's: report no verdict rather than a wrong one. A service that is
// not running is the same refusal.
func commandHealthTarget(group state.Group, svc *state.Service, h groups.HealthCheck, portMap map[string]int, gi, si int) (healthTarget, bool) {
	if !svc.Running || group.RootDir == nil {
		return healthTarget{}, false
	}
	// Resolving the token shells out to git; only a check that names
	// ${worktree} pays for it.
	worktree := ""
	if strings.Contains(h.Command, "${worktree}") {
		worktree = groups.WorktreeTokenFor(*group.RootDir)
	}
	expanded := groups.Expand(h.Command, groups.Values{
		Self: svc.Name, Worktree: worktree, Ports: portMap,
	})
	return healthTarget{
		kind: groups.HealthCommand, argv: spawn.SplitCmd(expanded),
		dir: filepath.Join(*group.RootDir, filepath.FromSlash(svc.Cwd)),
		env: commandEnv(svc.PortActual), group: gi, service: si,
	}, true
}

// notePortCheck remembers the first declaration for a listening port. A TCP
// check names its own port; an HTTP check names the port the service actually
// got. A later declaration for the same port does not replace the first.
func notePortCheck(checks map[int]portCheck, svc *state.Service, h groups.HealthCheck) {
	if svc.PortActual == nil {
		return
	}
	if h.Kind == groups.HealthTCP {
		if _, taken := checks[h.Port]; !taken {
			checks[h.Port] = portCheck{kind: groups.HealthTCP}
		}
		return
	}
	if _, taken := checks[*svc.PortActual]; taken {
		return
	}
	checks[*svc.PortActual] = portCheck{kind: groups.HealthHTTP, path: h.Path}
}

// listeningHealthTargets joins remembered port checks to the listener rows
// that can carry a verdict. A declared check whose port is down has no row,
// so it is not probed.
func listeningHealthTargets(rows []state.Port, checks map[int]portCheck) []healthTarget {
	byPort := make(map[int]*healthTarget)
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
	out := make([]healthTarget, 0, len(order))
	for _, port := range order {
		out = append(out, *byPort[port])
	}
	return out
}

// groupPortMap is the name-to-port map ${port} and ${<name>.port} expand
// against. A service that is not running contributes its declared port; a
// port: auto service contributes the port it actually got. `machine:`
// references contribute their fixed port, so a command check can name the
// database it is waiting for. Workspace references are not included: their
// address lives in another checkout and is resolved only when a service starts.
func groupPortMap(g state.Group) map[string]int {
	ports := make(map[string]int, len(g.Services)+len(g.Machine))
	for _, svc := range g.Services {
		switch {
		case svc.PortActual != nil:
			ports[svc.Name] = *svc.PortActual
		case svc.Port != nil:
			ports[svc.Name] = *svc.Port
		}
	}
	for _, m := range g.Machine {
		if _, exists := ports[m.Name]; !exists {
			ports[m.Name] = m.Port
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

// preferHost picks the loopback address to probe: a service bound on IPv4 (or
// on every address) is reached at 127.0.0.1, one that only ever bound IPv6 at
// [::1]. Choosing per port instead of per row is what keeps a dual-stack
// listener from being probed twice with two different answers.
func preferHost(current string, p state.Port) string {
	if current == "127.0.0.1" {
		return current
	}
	if !ipv6OnlyBind(p.BindAddress) {
		return "127.0.0.1"
	}
	if current == "" {
		return "[::1]"
	}
	return current
}

// ipv6OnlyBind reports whether an address a listener bound to can only be
// reached over IPv6.
func ipv6OnlyBind(bind string) bool {
	switch bind {
	case "::", "::1", "[::]", "[::1]":
		return true
	}
	return false
}
