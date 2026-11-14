package runtimesvc

import (
	"context"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/killer"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// The kill namespace. These methods stay thin on purpose: wire selectors are
// resolved against the runtime service's latest snapshot, then killer.KillPorts does the
// work — the very path `oberth kill` takes — so CLI, app and MCP cannot drift.
func init() {
	RegisterHandler("ports.kill", handlePortsKill)
	RegisterHandler("groups.kill", handleGroupsKill)
	RegisterCapability(rpc.CapabilityKillExpectRuns)
}

func handlePortsKill(ctx context.Context, req *Request) (any, error) {
	var p rpc.PortsKillParams
	err := req.Bind(&p)
	if err != nil {
		return nil, err
	}
	if len(p.Targets) == 0 {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "targets is required",
			`send {"targets": [{"port": 3000}]}`)
	}

	snap, err := killSnapshot(ctx, req)
	if err != nil {
		return nil, err
	}

	targets := make([]killer.Target, 0, len(p.Targets))
	for i := range p.Targets {
		t, err := selectorTarget(p.Targets[i], snap)
		if err != nil {
			return nil, killRPCError(err)
		}
		targets = append(targets, t)
	}

	opts := killer.Options{
		DryRun:   p.DryRun,
		Force:    p.Force,
		Escalate: p.Escalate,
		Tree:     p.Tree,
		Grace:    time.Duration(p.GraceMs) * time.Millisecond,
		Ports:    killerRows(snap),
	}
	opts.OnSignal = stopSignalObserver(req.Runtime, snap, targets, opts.DryRun, "")
	rows := killer.KillPorts(ctx, targets, opts)
	afterKill(req, opts.DryRun)
	return killEnvelope(rows), nil
}

// afterKill takes a fresh scan and publishes it, so ports that were just
// stopped are absent from the next read and their port_down rows reach the
// history ring even with no subscriber. Skipping it would leave the caller's
// own `oberth list` answering from the pre-kill snapshot.
func afterKill(req *Request, dryRun bool) (state.Snapshot, error) {
	if dryRun {
		return state.Snapshot{}, nil
	}
	// Imported runs that exited are reconciled before the post-stop snapshot is
	// published: this runtime service has no child reaper for them, and a stale Running
	// row would make the next restart skip a worker that is already gone.
	req.Runtime.Runs().Prune()
	return rescanAfterKill(req.Runtime)
}

// killSnapshot is the state that selectors and group members resolve against.
//
// It never reads the cache: up to CacheTTL of age would let a group that gained
// a service in that window be killed only partly, which the direct
// `oberth kill` path — scanning at the moment it is asked — never does. A kill
// is rare and deliberate, so one scan is the right price for acting on what is
// listening right now.
//
// That scan is bare. A kill only needs the pid and port of each listener, not a
// health verdict or a cpu reading, so no subscriber's `include` is inherited;
// the published snapshot carries those readings forward instead of collecting
// them again. That is what separates a dry run that answers in a moment from
// one that waits out `docker stats` and a round of health probes. Caller
// cancellation stops observation before selectors resolve; it does not cancel
// publication of a kill that already happened.
func killSnapshot(ctx context.Context, req *Request) (state.Snapshot, error) {
	snap, err := req.Runtime.Scanner.RescanContext(ctx, scanner.Include{})
	if err := ctx.Err(); err != nil {
		return state.Snapshot{}, err
	}
	if err != nil {
		return state.Snapshot{}, scanFailure(err)
	}
	return snap, nil
}

// killerRows hands the killer the scan the handler already took.
//
// Without it killer.KillPorts would scan the machine a second time from inside
// the RPC, so a single `ports.kill` cost two full scans: the §25
// rescan-before-select plus the killer's own. The second is pure duplication —
// the rows come from the same enriched pipeline (ports.Scan + Docker + Enrich)
// the runtime service's scanner ran a moment earlier — and it made a dry run of an
// unknown port take seconds. Every other caller of the killer (`oberth kill`,
// `kill-all`, `down`) already passes the snapshot its targets were resolved
// against; the runtime service does the same now, which also keeps the two paths'
// output identical.
func killerRows(snap state.Snapshot) []ports.ListeningPort {
	rows := state.ToListeningAll(snap.Ports)
	if rows != nil {
		return rows
	}
	// Never nil: a nil slice would tell the killer to scan for itself.
	return []ports.ListeningPort{}
}

// selectorTarget validates one wire selector and turns it into a killer target.
// When the snapshot binds a port to exactly one address, the target is pinned
// to it so a second listener appearing between scan and kill cannot widen the
// request; whatever the snapshot does not know about is passed through and
// comes back as a failed row rather than a failed call.
func selectorTarget(sel rpc.Selector, snap state.Snapshot) (killer.Target, error) {
	chosen := 0
	for _, present := range []bool{sel.Port != nil, sel.PID != nil, sel.RunID != nil} {
		if present {
			chosen++
		}
	}
	if chosen != 1 {
		return killer.Target{}, invalidSelector("a selector sets exactly one of port, pid and run_id",
			`bind_address only disambiguates port, for example {"port": 3000, "bind_address": "127.0.0.1"}`)
	}

	if sel.RunID != nil {
		return killer.Target{RunID: *sel.RunID}, nil
	}
	if sel.PID != nil {
		if err := checkSelectorPID(*sel.PID); err != nil {
			return killer.Target{}, err
		}
		return killer.Target{PID: *sel.PID}, nil
	}

	port := *sel.Port
	if err := checkSelectorPort(port); err != nil {
		return killer.Target{}, err
	}
	bindAddr := ""
	if sel.BindAddress != nil {
		bindAddr = *sel.BindAddress
	}
	if bindAddr == "" {
		if binds := bindAddresses(snap, port); len(binds) == 1 {
			bindAddr = binds[0]
		}
	}
	return killer.Target{Port: port, BindAddress: bindAddr}, nil
}

// invalidSelector builds the coded error every malformed selector earns.
func invalidSelector(detail, hint string) error {
	return &killer.CodedError{
		Code:   killer.CodeInvalidSelector,
		Detail: detail,
		Hint:   hint,
	}
}

func checkSelectorPID(pid int) error {
	if pid <= 0 {
		return &killer.CodedError{
			Code:   killer.CodeInvalidSelector,
			Detail: "pid must be positive",
		}
	}
	return nil
}

func checkSelectorPort(port int) error {
	if port <= 0 || port > 65535 {
		return &killer.CodedError{
			Code:   killer.CodeInvalidSelector,
			Detail: "port must be between 1 and 65535",
		}
	}
	return nil
}

// bindAddresses lists the distinct addresses a port is bound to in the snapshot.
func bindAddresses(snap state.Snapshot, port int) []string {
	var addresses []string
	known := make(map[string]bool)
	for _, p := range snap.Ports {
		if p.Port != port || known[p.BindAddress] {
			continue
		}
		known[p.BindAddress] = true
		addresses = append(addresses, p.BindAddress)
	}
	return addresses
}

// groupTargets resolves a group name against the snapshot. The resolved group
// is the primary signal; a run's group or id and a Docker Compose project are
// accepted too, matching what `oberth kill -g` accepts.
func groupTargets(snap state.Snapshot, name string) []killer.Target {
	return groupTargetsFor(snap, name, nil)
}

func groupTargetsFor(snap state.Snapshot, name string, only map[string]bool) []killer.Target {
	var out []killer.Target
	for _, p := range snap.Ports {
		if !inSnapshotGroup(p, name) {
			continue
		}
		if only != nil && (p.Run == nil || !only[p.Run.Name]) {
			continue
		}
		out = append(out, killer.Target{Port: p.Port, BindAddress: p.BindAddress})
	}
	return out
}

// expectedGroupTargets is the ownership-scoped port pass. A listener is a
// target only when it belongs to a run the caller named: a service whose live
// run is a different one was restarted by somebody else and is not this
// caller's to stop. A row the snapshot cannot attribute to a run, or whose run
// name contradicts the expectation, is left alone as well — an unverifiable
// identity cannot widen a stop.
func expectedGroupTargets(snap state.Snapshot, name string, expect map[string]string) []killer.Target {
	byRun := make(map[string]string, len(expect))
	for service, runID := range expect {
		byRun[runID] = service
	}
	var out []killer.Target
	for _, p := range snap.Ports {
		if p.Run == nil || !inSnapshotGroup(p, name) {
			continue
		}
		service, known := byRun[p.Run.ID]
		if !known || (p.Run.Name != "" && p.Run.Name != service) {
			continue
		}
		out = append(out, killer.Target{Port: p.Port, BindAddress: p.BindAddress})
	}
	return out
}

func inSnapshotGroup(p state.Port, name string) bool {
	names := make([]string, 0, 5)
	if p.Group != nil {
		names = append(names, *p.Group)
	}
	if p.Run != nil {
		names = append(names, p.Run.Group, p.Run.ID, p.Run.Name)
	}
	if p.Docker != nil {
		names = append(names, p.Docker.ComposeProject)
	}
	for _, candidate := range names {
		if candidate != "" && strings.EqualFold(candidate, name) {
			return true
		}
	}
	return false
}

// killEnvelope wraps the killer's rows in the mutation result. ok is true only
// when nothing failed, and affected lists the port key of every row that
// succeeded, once each.
func killEnvelope(rows []state.KillResult) rpc.KillEnvelope {
	results := rows
	if results == nil {
		results = []state.KillResult{}
	}
	env := rpc.KillEnvelope{Results: results}
	env.OK = true
	env.Affected = []string{}
	seen := make(map[string]bool, len(results))
	for i := range results {
		r := results[i]
		if r.OK {
			key := r.Key()
			if key != "" && !seen[key] {
				seen[key] = true
				env.Affected = append(env.Affected, key)
			}
			continue
		}
		// A not_found row is not a failure: the end state the caller asked for —
		// the thing is not running — already holds, so the envelope stays ok and
		// the row still appears in Results saying so.
		if r.Code != killer.CodeNotFound {
			env.OK = false
		}
	}
	return env
}

// killRPCError maps a killer error onto the contract's numeric error registry,
// carrying its hint through to error.data.hint.
func killRPCError(err error) error {
	return rpc.NewError(killErrorCode(killer.Code(err)), err.Error(), killer.Hint(err))
}

// killErrorCode translates the killer's classification into the numeric
// registry; an unrecognized one is an internal error.
func killErrorCode(class string) int {
	switch class {
	case killer.CodeNotFound:
		return rpc.CodeNotFound
	case killer.CodeAmbiguous:
		return rpc.CodeAmbiguous
	case killer.CodePermissionDenied:
		return rpc.CodePermission
	case killer.CodeInvalidSelector:
		return rpc.CodeInvalidSelector
	}
	return rpc.CodeInternal
}
