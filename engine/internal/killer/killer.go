// Package killer is option-berth's one implementation of "stop what is listening
// here". The CLI's `kill` and the daemon's kill requests both go through
// KillPorts, so these rules hold for every caller:
//
//   - A Docker-published port is stopped with `docker stop`, never signalled. A
//     port the container list claims while something else holds it is signalled
//     instead: the list can be stale, the listener cannot.
//   - A listener this engine started is stopped through its process group when
//     it leads one, otherwise through its process tree, so an
//     `npm → vite → esbuild` chain dies whole rather than at the leaf.
//   - A tree is signalled children before parents. That is a deterministic
//     order, not a promise to freeze a process tree the OS is still changing.
//   - SIGTERM first, then SIGKILL if the port is still listening once the grace
//     period is over — unless the caller opted out of escalation.
//   - A dry run reports exactly the actions a real run would take, and takes
//     none of them.
//
// A request answers with contract rows, one per action. No phase rewrites the
// request into a mutable result slice: a resolved unit carries the actions
// planned against it and, once they have run, their outcome.
package killer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/docker"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// DefaultGrace is how long a target gets to shut down after SIGTERM before the
// killer escalates to SIGKILL.
const DefaultGrace = 5 * time.Second

// pollInterval is how often the escalation wait re-checks a target. Polling is
// what keeps a well-behaved process from costing the whole grace period.
const pollInterval = 100 * time.Millisecond

// Target names one thing to stop. Exactly one of Port, PID and RunID is set;
// BindAddress only disambiguates a Port bound to several addresses.
//
// StartedAt is an optional identity claim on a PID target: when the caller
// believes the process started. Before signalling, the killer checks that the
// live process started no later than that, so a recycled pid is reported rather
// than killed. Zero means no claim — an explicit `kill --pid` says "this pid,
// now".
type Target struct {
	Port        int
	BindAddress string
	PID         int
	RunID       string
	StartedAt   time.Time
	// Name carries the caller's service label, never a selector or identity claim.
	Name string
}

// Options controls how targets are stopped. The zero value is the documented
// default: SIGTERM, escalate to SIGKILL after DefaultGrace, and no tree walk
// beyond the one an engine-started run already implies.
type Options struct {
	// Tree signals the target's whole process tree, not just the listener.
	// A listener attributed to a option-berth-started run is always treated as a
	// tree, with or without this flag.
	Tree bool
	// Force sends SIGKILL immediately and skips the escalation wait.
	Force bool
	// Grace is how long to wait after SIGTERM before escalating. Zero means
	// DefaultGrace.
	Grace time.Duration
	// Escalate opts out of SIGTERM → SIGKILL escalation when set to false.
	// Nil means the default, which is to escalate.
	Escalate *bool
	// DryRun plans the actions and performs none of them.
	DryRun bool
	// Ports is an optional pre-collected scan (enriched with Docker and
	// process information). When nil, KillPorts takes its own scan.
	Ports []ports.ListeningPort
	// OnSignal receives the unit's root PID synchronously after a process,
	// group or native-tree signal adapter returned success. It is not proof
	// of exit. Dry runs, refused identities and failed signals never notify.
	// Already accepted signals still notify if the request just cancelled.
	// This internal observer must return; it cannot authorize another action.
	OnSignal func(rootPID int)
}

// grace is the escalation window in effect.
func (o Options) grace() time.Duration {
	if o.Grace <= 0 {
		return DefaultGrace
	}
	return o.Grace
}

// escalating reports whether SIGTERM is followed by SIGKILL. A forced kill
// already sent the strongest signal, so there is nothing to escalate to.
func (o Options) escalating() bool {
	if o.Force {
		return false
	}
	return o.Escalate == nil || *o.Escalate
}

// Result is one row of the outcome: what was done, or — for a dry run — what
// would have been done, to one process or container.
type Result = state.KillResult

// Clock is the killer's view of time. It is replaceable so escalation can be
// tested without waiting out a real grace period.
//
// Waiting is part of the contract, not an add-on: the only wait this package
// makes is the poll between escalation probes, and it must end when the request
// does. A clock that can only sleep would leave a cancelled stop asleep for its
// whole grace period, so the interface requires the wait to be interruptible
// instead of discovering that capability by type assertion at the call site.
type Clock interface {
	Now() time.Time
	// Wait blocks for d and reports whether d elapsed. False means the request
	// was withdrawn first, and the caller must stop treating the target as
	// running.
	Wait(ctx context.Context, d time.Duration) bool
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// engine holds the machine-facing dependencies so tests can substitute them.
type engine struct {
	ctx        context.Context
	tableErr   error
	table      ProcessTable
	clock      Clock
	signalProc func(pid int, force bool) error
	signalGrp  func(pgid int, force bool) error
	signalTree func(pid int, force bool) error
	groupOf    func(pid int) (int, bool)
	alive      func(pid int) bool
	// startTime reports when the process behind a pid started, for the
	// recycle check on identity-claiming targets (ports.ProcessStartFreshContext).
	startTime  func(pid int) (time.Time, bool)
	probe      func(port int, bind string) portState
	dockerStop func(container string) error
	nativeTree bool
	onSignal   func(int)
}

func newEngine(ctx context.Context) *engine {
	table, err := scanProcessTableContext(ctx)
	return &engine{
		ctx:        ctx,
		tableErr:   err,
		table:      table,
		clock:      realClock{},
		signalProc: func(pid int, force bool) error { return signalProcessContext(ctx, pid, force) },
		signalGrp:  func(pid int, force bool) error { return signalGroupContext(ctx, pid, force) },
		signalTree: func(pid int, force bool) error { return signalTreeContext(ctx, pid, force) },
		groupOf:    processGroup,
		alive:      pidAlive,
		startTime:  func(pid int) (time.Time, bool) { return ports.ProcessStartFreshContext(ctx, pid) },
		probe:      func(port int, bind string) portState { return probePortContext(ctx, port, bind) },
		dockerStop: func(name string) error { return docker.StopContainerContext(ctx, name) },
		nativeTree: hasNativeTreeKill(),
	}
}

// KillPorts stops every target and returns one row per action, children before
// parents. It returns no error: a target that could not be resolved or
// signalled comes back as a row with OK false, so a partial failure is reported
// per target instead of hiding the targets that did stop.
func KillPorts(ctx context.Context, targets []Target, opts Options) []Result {
	if len(targets) == 0 {
		return []Result{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// One request budget, including collection and commands; grace is not
	// restarted per target. In-flight synchronous syscalls still must return.
	budget := controlBudget(opts)
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return failedTargets(targets, err)
	}
	snapshot := opts.Ports
	if snapshot == nil {
		var err error
		snapshot, err = scanPortsContext(ctx)
		if err != nil {
			return failedTargets(targets, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return failedTargets(targets, err)
	}
	rows := newEngine(ctx).kill(ctx, snapshot, targets, opts)
	for i := range rows {
		rows[i].Host = state.LocalhostName
	}
	return rows
}

// failedTargets is the answer when nothing could even be attempted: one row per
// target, each carrying why. Name stays the target's run id — the caller never
// reached a listener, so there is no display name to report.
func failedTargets(targets []Target, err error) []Result {
	out := make([]Result, 0, len(targets))
	for _, t := range targets {
		out = append(out, Result{Host: state.LocalhostName, Port: t.Port,
			BindAddress: t.BindAddress, PID: t.PID, Name: t.RunID,
			Method: state.MethodNone, Error: err.Error(), Code: Code(err)})
	}
	return out
}

// Collection failure is not an empty successful observation. Context reaches
// listeners, Docker and metadata without adding a detached worker.
func scanPortsContext(ctx context.Context) ([]ports.ListeningPort, error) {
	found, err := ports.ScanContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := docker.EnrichPortsContext(ctx, found); err != nil {
		return nil, err
	}
	if err := ports.EnrichContext(ctx, found); err != nil {
		return nil, err
	}
	return found, ctx.Err()
}

// unit is one resolved stoppable: a container, a process group, or a process
// tree handed over as an ordered pid list (children first, root last).
//
// Resolution is over by the time a unit exists, so the only thing that changes
// afterwards is stops: what the run plans to do to this unit and what came of
// it. A refused target is a unit too, with a refusal and no stops to run.
type unit struct {
	tree      bool
	claims    map[int]time.Time // recorded run/listener evidence, never replaced
	observed  map[int]time.Time // live births captured for this plan only
	port      int
	bind      string
	name      string
	container string // non-empty for a Docker target
	pgid      int    // non-zero when the whole process group is signalled
	pids      []int  // children-first order; ignored when pgid or container is set
	root      int    // the listener (or run root) this unit is anchored on
	listenPID int    // the pid actually holding the socket, when there is one
	depth     int    // ancestry depth of root, used to order units children-first
	refusal   error  // resolution said no: one failed row, nothing signalled
	stops     []stop // one per planned action, filled by plan, settled by the run
}

// stop is one action the run planned against a unit, with its outcome. A group
// or container unit has exactly one; a process unit has one per pid. Keeping the
// outcome here is what lets the phases stop looking their row up again.
type stop struct {
	pid    int
	name   string
	method state.KillMethod
	ok     bool
	err    error
}

// kill answers one stop request: resolve, order, plan, act, escalate, then
// project. Nothing outside the units is mutated along the way.
func (e *engine) kill(ctx context.Context, snapshot []ports.ListeningPort, targets []Target, opts Options) []Result {
	if ctx == nil {
		ctx = context.Background()
	}
	e.ctx = ctx
	e.onSignal = opts.OnSignal
	if err := ctx.Err(); err != nil {
		return failedTargets(targets, err)
	}

	var units []*unit
	for _, t := range targets {
		units = append(units, e.resolve(t, snapshot, opts)...)
	}
	for _, u := range units {
		e.captureIdentity(u)
	}
	units = e.dedupe(units)
	e.order(units)

	for _, u := range units {
		e.plan(u, opts)
	}
	if !opts.DryRun {
		for _, u := range units {
			e.execute(u, opts)
		}
		if opts.escalating() {
			e.escalate(ctx, units, opts)
		}
	}
	return project(units)
}

// project turns the settled units into contract rows: units outward-in, and
// within a unit the order the run planned, which is children before parents.
func project(units []*unit) []Result {
	total := 0
	for _, u := range units {
		total += len(u.stops)
	}
	rows := make([]Result, 0, total)
	for _, u := range units {
		for _, s := range u.stops {
			row := Result{
				Port: u.port, BindAddress: u.bind,
				PID: s.pid, Name: s.name,
				Method: s.method, OK: s.ok,
			}
			if s.err != nil {
				row.Error = s.err.Error()
				row.Code = Code(s.err)
			}
			rows = append(rows, row)
		}
	}
	return rows
}

// resolve turns one target into the units it addresses. A run id can address
// several listeners, so the answer is a slice; a target that matches nothing
// comes back as one refusing unit, so the caller still gets a row that says why.
func (e *engine) resolve(t Target, snapshot []ports.ListeningPort, opts Options) []*unit {
	units := e.unitsFor(t, snapshot, opts)
	// The caller's own label is presentation, applied only after every identity
	// check has run: naming a target must not change which process it names.
	if t.Name != "" {
		for _, u := range units {
			u.name = t.Name
		}
	}
	return units
}

// unitsFor dispatches on which selector the target set.
func (e *engine) unitsFor(t Target, snapshot []ports.ListeningPort, opts Options) []*unit {
	switch {
	case t.RunID != "":
		return e.unitsForRun(t, snapshot, opts)
	case t.Port > 0:
		return e.unitsForPort(t, snapshot, opts)
	case t.PID > 0:
		return e.unitsForPID(t, snapshot, opts)
	}
	return refusing(&unit{}, codedf(CodeInvalidSelector, "", "empty target: set one of port, pid or run_id"))
}

// refusing is the one way resolution says no: a unit that plans a single failed
// row and signals nothing.
func refusing(shape *unit, err error) []*unit {
	shape.refusal = err
	return []*unit{shape}
}

// unitsForRun addresses every listener the run recorded.
func (e *engine) unitsForRun(t Target, snapshot []ports.ListeningPort, opts Options) []*unit {
	var units []*unit
	for i := range snapshot {
		if snapshot[i].RunID == t.RunID || snapshot[i].Tag == t.RunID {
			units = append(units, e.unitFor(&snapshot[i], opts))
		}
	}
	if len(units) == 0 {
		return refusing(&unit{name: t.RunID},
			codedf(CodeNotFound, "", "no listening port belongs to run %s", t.RunID))
	}
	return units
}

// unitsForPort addresses the listener on one port. A port bound to several
// addresses is ambiguous on purpose: picking one would mean guessing which
// process the caller meant, and the caller can say which with --ip.
func (e *engine) unitsForPort(t Target, snapshot []ports.ListeningPort, opts Options) []*unit {
	matches := matchPort(snapshot, t.Port, t.BindAddress)
	switch len(matches) {
	case 0:
		return refusing(&unit{port: t.Port, bind: t.BindAddress},
			codedf(CodeNotFound, "", "no process is listening on %s", where(t.Port, t.BindAddress)))
	case 1:
		return []*unit{e.unitFor(matches[0], opts)}
	}
	return refusing(&unit{port: t.Port}, ambiguousPort(t.Port, matches))
}

// where spells a port with its bind address when one was named.
func where(port int, bind string) string {
	if bind == "" {
		return strconv.Itoa(port)
	}
	return bind + ":" + strconv.Itoa(port)
}

// ambiguousPort names every address the port is bound to and how to pick one.
func ambiguousPort(port int, matches []*ports.ListeningPort) *CodedError {
	addresses := make([]string, 0, len(matches))
	for _, match := range matches {
		addresses = append(addresses, match.BindAddress)
	}
	return codedf(CodeAmbiguous, "re-run with --ip "+addresses[0],
		"port %d is bound to %d addresses: %s", port, len(addresses), strings.Join(addresses, ", "))
}

// unitsForPID addresses one pid. A pid the scan knows about is stopped as the
// listener it is; otherwise its liveness is the only thing that can be
// checked, and its tree is what gets signalled.
func (e *engine) unitsForPID(t Target, snapshot []ports.ListeningPort, opts Options) []*unit {
	if !validSignalPID(t.PID) {
		return refusing(&unit{root: t.PID}, codedf(CodeInvalidSelector, "", "invalid PID %d", t.PID))
	}
	if scanned := e.scannedPID(t.PID, snapshot, opts); scanned != nil {
		// The identity claim survives even when the pid matched a listener:
		// selection is not the final signal boundary, and a listener must not
		// erase the run evidence attached to the pid.
		scanned.addClaim(t.PID, t.StartedAt)
		return []*unit{scanned}
	}
	if !e.alive(t.PID) {
		return refusing(&unit{root: t.PID}, codedf(CodeNotFound, "", "no process with PID %d", t.PID))
	}
	u := &unit{root: t.PID, name: e.table.Name(t.PID), listenPID: t.PID}
	e.fillProcesses(u, t.PID, opts.Tree)
	u.addClaim(t.PID, t.StartedAt)
	return []*unit{u}
}

// scannedPID is the unit for a pid that also appears in the listener scan, or
// nil when it does not.
func (e *engine) scannedPID(pid int, snapshot []ports.ListeningPort, opts Options) *unit {
	for i := range snapshot {
		if snapshot[i].PID == pid {
			return e.unitFor(&snapshot[i], opts)
		}
	}
	return nil
}

// unitFor builds the unit for a scanned listener.
func (e *engine) unitFor(lp *ports.ListeningPort, opts Options) *unit {
	u := &unit{port: lp.Port, bind: lp.BindAddress, name: lp.DisplayName(), listenPID: lp.PID}
	if !state.IsLocalhost(lp.Host) {
		u.refusal = codedf(CodeInvalidSelector, "", "refusing to signal a listener from another host")
		return u
	}
	if born, err := time.Parse(time.RFC3339, lp.StartedAt); err == nil {
		u.addClaim(lp.PID, born)
	}

	if lp.Type == ports.PortTypeDocker && lp.DockerContainer != "" {
		u.container = lp.DockerContainer
		return u
	}
	if !validSignalPID(lp.PID) {
		u.refusal = &CodedError{
			Code:   CodePermissionDenied,
			Detail: fmt.Sprintf("could not resolve the process listening on port %d", lp.Port),
			Hint:   "re-run with sudo for full visibility",
		}
		return u
	}

	// A listener this engine started is stopped as a whole run: its process
	// group when it leads one, otherwise its tree. The supervisor and its
	// workers go together either way.
	root, tree := lp.PID, opts.Tree
	if lp.RunRootPID > 0 {
		root, tree = lp.RunRootPID, true
	}
	if !validSignalPID(root) {
		u.refusal = codedf(CodeInvalidSelector, "", "invalid run root PID %d", root)
		return u
	}
	u.root = root
	if pgid, ok := e.groupOf(root); ok {
		u.pgid = pgid
		u.depth = len(e.table.Ancestors(root))
		return u
	}
	e.fillProcesses(u, root, tree)
	return u
}

// fillProcesses records the pids this unit will signal, children first.
func (e *engine) fillProcesses(u *unit, root int, tree bool) {
	u.root, u.tree = root, tree
	if tree && !e.nativeTree && e.tableErr != nil {
		u.refusal = e.tableErr
		return
	}
	u.depth = len(e.table.Ancestors(root))
	u.pids = e.processOrder(root, tree)
}

// processOrder is the pid list one unit signals: the root alone, or the root
// with everything below it. Windows hands the whole tree to `taskkill /T`,
// which has no PPID table for us to walk and no order for us to impose.
func (e *engine) processOrder(root int, tree bool) []int {
	if !tree || e.nativeTree {
		return []int{root}
	}
	if pids := e.table.Descendants(root); len(pids) > 0 {
		return pids
	}
	return []int{root}
}

// dedupe drops pids an earlier unit already claimed, so overlapping targets — a
// port and its own child port, `--all` over one tree — signal each process
// exactly once. A refusal and a container claim nothing and always survive.
func (e *engine) dedupe(units []*unit) []*unit {
	claimedPIDs := map[int]bool{}
	claimedGroups := map[int]bool{}
	kept := make([]*unit, 0, len(units))
	for _, u := range units {
		switch {
		case u.refusal != nil, u.container != "":
			kept = append(kept, u)
		case u.pgid != 0:
			if claimedGroups[u.pgid] {
				continue
			}
			claimedGroups[u.pgid] = true
			claimedPIDs[u.pgid] = true
			kept = append(kept, u)
		default:
			fresh := unclaimed(u.pids, claimedPIDs)
			if len(fresh) == 0 {
				continue
			}
			u.pids = fresh
			kept = append(kept, u)
		}
	}
	return kept
}

// unclaimed marks every pid it has not seen yet and returns those in order.
func unclaimed(pids []int, claimed map[int]bool) []int {
	fresh := make([]int, 0, len(pids))
	for _, pid := range pids {
		if claimed[pid] {
			continue
		}
		claimed[pid] = true
		fresh = append(fresh, pid)
	}
	return fresh
}

// order sorts units so a unit rooted deeper in the process tree runs before its
// own ancestor: children before parents across targets, not just within one.
// The sort is stable, so unrelated targets keep the caller's order.
func (e *engine) order(units []*unit) {
	sort.SliceStable(units, func(i, j int) bool { return units[i].depth > units[j].depth })
}

// plan turns a resolved unit into the actions it will take and names each row
// the way a reader expects: the scanner's display name for the process holding
// the socket, the process table's name for the rest of a tree, the caller's
// label for a group or container.
func (e *engine) plan(u *unit, opts Options) {
	if u.refusal != nil {
		u.stops = []stop{{pid: u.root, name: u.name, method: state.MethodNone, err: u.refusal}}
		return
	}
	if u.container != "" {
		u.stops = []stop{{name: u.name, method: state.MethodDockerStop, ok: true}}
		return
	}
	method := signalMethod(opts.Force)
	if u.pgid != 0 {
		u.stops = []stop{{pid: u.pgid, name: u.name, method: method, ok: true}}
		return
	}
	u.stops = make([]stop, 0, len(u.pids))
	for _, pid := range u.pids {
		name := e.table.Name(pid)
		if u.name != "" && (pid == u.listenPID || pid == u.root) {
			name = u.name
		}
		u.stops = append(u.stops, stop{pid: pid, name: name, method: method, ok: true})
	}
}

// execute performs the planned actions for one unit. A refused identity clears
// the method as well as the row, because no signal was sent: reporting sigterm
// there would claim an action that never happened.
func (e *engine) execute(u *unit, opts Options) {
	if u.refusal != nil {
		return
	}
	settle := func(s *stop, err error) {
		if err == nil {
			return
		}
		s.ok = false
		s.err = err
	}
	refuse := func(s *stop, err error) {
		settle(s, err)
		s.method = state.MethodNone
	}

	switch {
	case u.container != "":
		first := &u.stops[0]
		if err := e.guard(u, 0); err != nil {
			refuse(first, err)
			return
		}
		settle(first, e.dockerStop(u.container))
	case u.pgid != 0:
		first := &u.stops[0]
		if err := e.guard(u, u.pgid); err != nil {
			refuse(first, err)
			return
		}
		settle(first, e.signalResult(u, e.signalGrp(u.pgid, opts.Force)))
	default:
		for i, pid := range u.pids {
			s := &u.stops[i]
			if err := e.guard(u, pid); err != nil {
				refuse(s, err)
				continue
			}
			if e.nativeTree && u.tree {
				settle(s, e.signalResult(u, e.signalTree(pid, opts.Force)))
			} else {
				settle(s, e.signalResult(u, e.signalProc(pid, opts.Force)))
			}
		}
	}
}

// signalMethod names the signal a non-escalated kill sends.
func signalMethod(force bool) state.KillMethod {
	if force {
		return state.MethodSIGKILL
	}
	return state.MethodSIGTERM
}

// matchPort returns the scanned listeners on a port, filtered by bind address
// when one was given.
func matchPort(snapshot []ports.ListeningPort, port int, bind string) []*ports.ListeningPort {
	var out []*ports.ListeningPort
	for i := range snapshot {
		if snapshot[i].Port != port {
			continue
		}
		if bind != "" && snapshot[i].BindAddress != bind {
			continue
		}
		out = append(out, &snapshot[i])
	}
	return out
}

// portState is what one connection attempt can establish about a port. It is
// three-valued on purpose, because "could not connect" is two different facts.
type portState int

const (
	// portClosed is a refused connection: the kernel answered for a port with
	// no listener, so nothing is holding it any more.
	portClosed portState = iota
	// portOpen is an accepted connection: something is listening and answering.
	portOpen
	// portUnknown is an attempt that timed out. The listener is there and not
	// answering — a full accept queue, because nobody is calling accept — which
	// is exactly the state a leftover gets into. Reading this as "the port is
	// free" is what used to make `kill` report success while the row was still
	// on the table.
	portUnknown
)

// probeTimeout bounds one probe. A hung connect is the signal we are looking
// for, so waiting much longer buys nothing.
const probeTimeout = 300 * time.Millisecond

// probePort reports what one TCP connection to a port establishes. A wildcard
// bind is probed on loopback, which is where a dev server is reached from
// anyway.
func probePort(port int, bind string) portState {
	return probeWith(net.DialTimeout, port, bind)
}

// probeWith is probePort with the dialer injected, so the three outcomes can be
// tested without a machine that can produce a hung listener on demand.
func probeWith(dial func(network, address string, timeout time.Duration) (net.Conn, error),
	port int, bind string) portState {
	host := bind
	switch host {
	case "", "0.0.0.0", "*":
		host = "127.0.0.1"
	case "::", "[::]":
		host = "::1"
	}
	conn, err := dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)), probeTimeout)
	if err == nil {
		_ = conn.Close()
		return portOpen
	}
	// A timeout is not a refusal: the listener is still there, it just is not
	// answering. Everything else (a refusal above all) is the kernel telling us
	// nobody is listening.
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return portUnknown
	}
	return portClosed
}

// Alive reports whether a process with this pid currently exists. Callers use
// it to tell a pid from a port when a bare number could be either.
func Alive(pid int) bool { return pidAlive(pid) }
