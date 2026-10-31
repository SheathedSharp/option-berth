// Package groupstart serves `groups.start`: it walks a `oberth.yaml`'s
// services in dependency order and spawns each one detached, streaming a chunk
// per service as it goes (contract §1).
//
// It lives outside internal/daemon because starting a service needs the run
// registry, and the daemon package must not import it (contract §8). Linking
// this package in is what makes `groups.start` exist; internal/cmd does that
// for the `option-berth` binary.
package groupstart

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/runsreg"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/spawn"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// DependencyTimeout bounds how long one service waits for something it
// depends_on to become ready. Ported services become ready when they listen;
// no-port services become ready when their run is live. On a timeout the
// dependent service is reported and skipped, and the independent ones still
// start: a slow database must not mean nothing came up.
const DependencyTimeout = 30 * time.Second

// dependencyTimeout is the value actually used, so tests do not have to wait
// out the real one to see a timeout reported.
var dependencyTimeout = DependencyTimeout

// dependencyPoll is how often the wait re-reads the daemon's state. The
// scanner's own cache means a poll costs nothing until it is stale.
const dependencyPoll = 250 * time.Millisecond

// startupCheckWindow catches commands that fail before they can become a
// service (missing executable, bind failure, or an immediate exit) without
// delaying healthy services that take a little time to bind their port.
const startupCheckWindow = 100 * time.Millisecond

func init() {
	daemon.RegisterHandler("groups.start", handleGroupsStart)
	daemon.RegisterCapability("groups")
}

func handleGroupsStart(ctx context.Context, req *daemon.Request) (any, error) {
	var p rpc.GroupsStartParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	cfg, err := resolveConfig(req.Runtime, p)
	if err != nil {
		return nil, err
	}
	// The group the services run under is the one the file's ports are
	// published in: `<project>@<worktree>` for a worktree's copy of a
	// committed file, never the `name:` the copy carries (step 5A.6).
	group := req.Runtime.Scanner.GroupOf(cfg)
	plan, err := groups.Plan(cfg, p.Only)
	if err != nil {
		var unknown *groups.UnknownServiceError
		if errors.As(err, &unknown) {
			return nil, rpc.NewError(rpc.CodeNotFound, unknown.Error(),
				"run `oberth status` to see the services this worktree declares")
		}
		return nil, rpc.NewError(rpc.CodeInternal, err.Error(), "")
	}
	if len(plan) == 0 {
		return nil, rpc.NewError(rpc.CodeInvalidParams,
			cfg.Path+" declares no services to start",
			"add a `services:` list, or start the command yourself with `oberth start`")
	}

	rt := req.Runtime
	// One id for everything this call starts, so a client can tell the
	// services it brought up from the ones that were already running.
	meta := runsreg.Meta{ConfigPath: cfg.Path, StartID: spawn.NewID(), Origin: runsreg.Origin(req)}
	initial := rpc.GroupsStartResult{
		MutationResult: rpc.MutationResult{OK: true, Affected: []string{}},
		StartID:        meta.StartID,
	}
	return daemon.StartStream(ctx, req, initial, func(ctx context.Context, s *daemon.Stream) (any, error) {
		// One start per group at a time. Two concurrent `up` calls would
		// otherwise both pass alreadyRunning before either registers a run,
		// and the loser of the race would report a bind failure for a service
		// that is already coming up.
		release, err := rt.AcquireGroup(ctx, group)
		if err != nil {
			return nil, err
		}
		defer release()
		return run(ctx, rt, s, cfg, group, plan, p, meta)
	})
}

// run starts the planned services one at a time, sending a chunk per service.
// A service that fails never stops the ones after it: the caller asked for the
// group, and a partial group is more useful than none.
func run(ctx context.Context, rt *daemon.Runtime, s *daemon.Stream,
	cfg *groups.Config, group string, plan []groups.Step, p rpc.GroupsStartParams,
	meta runsreg.Meta) (rpc.GroupsStartEnd, error) {

	end := rpc.GroupsStartEnd{Started: []string{}, Skipped: []string{}, Errors: []string{}}
	changed := false
	defer func() {
		if changed || len(end.Errors) > 0 {
			rt.Scanner.Invalidate()
		}
	}()
	book, err := newAddressBook(ctx, rt, cfg, group)
	if err != nil {
		return end, err
	}

	for _, step := range plan {
		if err := ctx.Err(); err != nil {
			return end, err
		}
		svc := step.Service
		reason, runID, up, err := alreadyRunning(ctx, rt, group, svc)
		if err != nil {
			return end, err
		}
		if up {
			if err := s.Send(rpc.GroupsStartChunk{
				Service: svc.Name, State: "skipped", Skipped: true, Reason: reason, RunID: runID,
			}); err != nil {
				return end, err
			}
			end.Skipped = append(end.Skipped, svc.Name)
			continue
		}

		if err := waitFor(ctx, rt, group, step.Waits, book, meta.StartID); err != nil {
			if err := ctx.Err(); err != nil {
				return end, err
			}
			message := detail(err)
			recordStartFailureReason(cfg, group, svc, meta, startFailureReason(message), message)
			end.Errors = append(end.Errors, svc.Name)
			rt.Scanner.Wake()
			if err := s.Send(rpc.GroupsStartChunk{
				Service: svc.Name, State: "failed", Reason: startFailureReason(message),
				Error: message, Hint: dependencyHint(err),
			}); err != nil {
				return end, err
			}
			continue
		}

		if strings.TrimSpace(svc.Prepare) != "" {
			// The skip is an optimization, never a substitute for running the
			// step when the evidence is incomplete. CheckCwd failing here means
			// prepare() below will report the refusal with its own hint.
			skipped := false
			if cwd, err := runsreg.CheckCwd(cfg.ServiceDir(svc), p.AllowOutsideHome); err == nil &&
				prepareUnchanged(cfg.Path, svc, cwd) {
				rt.Logger.Info("prepare inputs unchanged; skipping", "group", group, "service", svc.Name)
				skipped = true
			}
			if !skipped {
				if err := prepare(ctx, cfg, group, svc, book, p); err != nil {
					// Losing the request is not evidence that the service failed.
					// prepare has already killed and reaped its attached child.
					if err := ctx.Err(); err != nil {
						return end, err
					}
					rt.Logger.Warn("preparing a service", "group", group, "service", svc.Name, "error", err)
					// Same rule as the cmd path: the error's own hint, or none. A
					// prepare step that failed before its log file was even created
					// has no log to point at.
					message, hint := detailAndHint(err)
					recordStartFailure(cfg, group, svc, meta, "prepare: "+message)
					end.Errors = append(end.Errors, svc.Name)
					rt.Scanner.Wake()
					if err := s.Send(rpc.GroupsStartChunk{
						Service: svc.Name, State: "failed", Reason: "start_failed",
						Error: "prepare failed: " + message, Hint: hint,
					}); err != nil {
						return end, err
					}
					continue
				}
				// The step succeeded: remember exactly what it saw. A stamp
				// write failure is not a start failure — the service can still
				// run and the next start simply repeats prepare.
				if cwd, err := runsreg.CheckCwd(cfg.ServiceDir(svc), p.AllowOutsideHome); err == nil {
					if err := recordPrepareStamp(cfg.Path, svc, cwd); err != nil {
						rt.Logger.Warn("recording prepare inputs", "group", group, "service", svc.Name, "error", err)
					}
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return end, err
		}
		h, err := start(ctx, rt, cfg, group, svc, book, p, meta)
		if err != nil {
			if err := ctx.Err(); err != nil {
				return end, err
			}
			rt.Logger.Warn("starting a service", "group", group, "service", svc.Name, "error", err)
			// The hint is the error's own, never a guess. Every failure on this
			// path happened before a process existed, so a "read the logs" hint
			// would name a file that was never written; the refusals that do
			// have a next step carry it themselves.
			message, hint := detailAndHint(err)
			recordStartFailureReason(cfg, group, svc, meta, startFailureReason(message), message)
			end.Errors = append(end.Errors, svc.Name)
			rt.Scanner.Wake()
			if err := s.Send(rpc.GroupsStartChunk{
				Service: svc.Name, State: "failed", Reason: startFailureReason(message),
				Error: message, Hint: hint,
			}); err != nil {
				return end, err
			}
			continue
		}
		changed = true
		// A detached run is already registered. Cancelling the observation
		// stops this plan, not that run; its reaper remains the sole exit owner.
		timer := time.NewTimer(startupCheckWindow)
		select {
		case <-ctx.Done():
			timer.Stop()
			return end, ctx.Err()
		case <-timer.C:
		}
		if err := ctx.Err(); err != nil {
			return end, err
		}
		if exit, ok := runsreg.Default.ExitForRun(h.ID, h.PID, h.StartedAt); ok {
			end.Errors = append(end.Errors, svc.Name)
			if err := s.Send(rpc.GroupsStartChunk{
				Service: svc.Name, State: "failed", Reason: exit.Reason,
				Error: fmt.Sprintf("service exited with code %d (%s)", exit.Code, exit.Reason),
				Hint:  logsHint(svc.Name), PID: h.PID, RunID: h.ID, LogPath: h.LogPath,
			}); err != nil {
				return end, err
			}
			continue
		}
		end.Started = append(end.Started, svc.Name)
		if err := s.Send(rpc.GroupsStartChunk{
			Service: svc.Name, State: "started", PID: h.PID, Port: h.PortHint, RunID: h.ID,
			LogPath: h.LogPath, LogOffset: h.LogOffset,
		}); err != nil {
			return end, err
		}
	}
	return end, nil
}

// prepare runs a service's optional build/code-generation step to completion
// before the long-lived service process is spawned. It is deliberately not a
// registered run: the manifest service remains the one thing `up` starts and
// `down` stops. Preparation output is appended to that service's log so a
// failed build is inspectable with `oberth logs <service> --once`.
func prepare(ctx context.Context, cfg *groups.Config, group string,
	svc groups.Service, book *addressBook, p rpc.GroupsStartParams) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	argv := spawn.SplitCmd(svc.Prepare)
	if len(argv) == 0 {
		return fmt.Errorf("service %s has an empty prepare command", svc.Name)
	}
	ports, err := book.forService(svc)
	if err != nil {
		return err
	}
	values := groups.Values{Self: svc.Name, Worktree: book.worktree, Ports: ports}
	for i := range argv {
		argv[i] = groups.Expand(argv[i], values)
	}
	cwd, err := runsreg.CheckCwd(cfg.ServiceDir(svc), p.AllowOutsideHome)
	if err != nil {
		return err
	}
	logPath := spawn.LogPath(group, svc.Name)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return fmt.Errorf("creating the log directory: %w", err)
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", logPath, err)
	}
	defer log.Close()
	env, err := serviceEnv(p.Env, cfg, svc, ports[svc.Name], ports, values)
	if err != nil {
		return err
	}
	h, err := spawn.Spawn(ctx, spawn.Request{
		Argv:   argv,
		Cwd:    cwd,
		Env:    env,
		Group:  group,
		Name:   svc.Name,
		Stdout: log,
		Stderr: log,
	})
	if err != nil {
		return err
	}
	done := make(chan struct {
		code int
		err  error
	}, 1)
	go func() {
		code, waitErr := h.Wait()
		done <- struct {
			code int
			err  error
		}{code, waitErr}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			return result.err
		}
		if result.code != 0 {
			return fmt.Errorf("prepare command exited with code %d", result.code)
		}
		return nil
	case <-ctx.Done():
		_ = h.Kill()
		<-done
		return ctx.Err()
	}
}

func logsHint(service string) string { return "oberth logs " + service + " --once" }

func startFailureReason(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "timed out"):
		return "dependency_timeout"
	case strings.Contains(lower, "dependency") || strings.Contains(lower, "no port for"):
		return "dependency_not_ready"
	case strings.Contains(lower, "address already in use") || strings.Contains(lower, "eaddrinuse"):
		return "port_occupied"
	default:
		return "start_failed"
	}
}

func recordStartFailure(cfg *groups.Config, group string, svc groups.Service, meta runsreg.Meta, message string) {
	recordStartFailureReason(cfg, group, svc, meta, runsreg.ReasonStartFailed, message)
}

func recordStartFailureReason(cfg *groups.Config, group string, svc groups.Service, meta runsreg.Meta, reason, message string) {
	runsreg.Default.StartFailedWithReason(runsreg.Record{
		ID:         spawn.NewID(),
		Group:      group,
		Name:       svc.Name,
		Cmd:        svc.Cmd,
		Cwd:        cfg.ServiceDir(svc),
		PortHint:   svc.Port,
		ConfigPath: meta.ConfigPath,
		StartID:    meta.StartID,
		Origin:     meta.Origin,
		SpecHash:   groups.ServiceSpecHash(svc),
		LogPath:    spawn.LogPath(group, svc.Name),
	}, reason, message)
}

// start spawns one service through the run registry, so the ports it opens are
// attributed to this group and this service name. Its references are expanded
// and its environment built here, once every port it names is known.
func start(ctx context.Context, rt *daemon.Runtime, cfg *groups.Config, group string,
	svc groups.Service, book *addressBook, p rpc.GroupsStartParams,
	meta runsreg.Meta) (*spawn.Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	argv := spawn.SplitCmd(svc.Cmd)
	if len(argv) == 0 {
		return nil, fmt.Errorf("service %s has no cmd to run", svc.Name)
	}
	ports, err := book.forService(svc)
	if err != nil {
		return nil, err
	}
	// Expanded after splitting, so a value can never change how the command
	// splits into arguments.
	values := groups.Values{Self: svc.Name, Worktree: book.worktree, Ports: ports}
	for i := range argv {
		argv[i] = groups.Expand(argv[i], values)
	}
	cwd, err := runsreg.CheckCwd(cfg.ServiceDir(svc), p.AllowOutsideHome)
	if err != nil {
		return nil, err
	}
	port := ports[svc.Name]
	runMeta := meta
	runMeta.SpecHash = groups.ServiceSpecHash(svc)
	env, err := serviceEnv(p.Env, cfg, svc, port, ports, values)
	if err != nil {
		return nil, err
	}
	return runsreg.Spawn(ctx, rt, spawn.Request{
		Argv:     argv,
		Cwd:      cwd,
		Env:      env,
		Group:    group,
		Name:     svc.Name,
		PortHint: port,
		LogPath:  spawn.LogPath(group, svc.Name),
	}, runMeta)
}

// serviceEnv is the environment a service starts in, each layer winning over
// the one before: the daemon's own, the caller's (the CLI sends its shell's),
// the service's `env_file`, PORT for a service with a port, and the service's
// own `env:` with its references expanded. BERTH_PORT and the other run
// variables are added by spawn on top of all of it.
//
// The env file is read at start time, so the same committed manifest can point
// every worktree at one file outside their checkouts. Its values are data: they
// are not reference-expanded and never appear in status or logs.
func serviceEnv(caller map[string]string, cfg *groups.Config, svc groups.Service, port int, ports map[string]int, values groups.Values) ([]string, error) {
	over := make(map[string]string, len(caller)+len(svc.Env)+1)
	for k, v := range caller {
		over[k] = v
	}
	if path := cfg.EnvFilePath(svc); path != "" {
		fromFile, err := groups.EnvFileValues(path)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", svc.Name, err)
		}
		for k, v := range fromFile {
			over[k] = v
		}
	}
	if port > 0 {
		over["PORT"] = strconv.Itoa(port)
	}
	for k, v := range svc.Env {
		over[k] = groups.Expand(v, values)
	}
	return layer(os.Environ(), over), nil
}

// layer returns base with every key in over replaced or added.
func layer(base []string, over map[string]string) []string {
	out := make([]string, 0, len(base)+len(over))
	for _, kv := range base {
		if key, _, ok := strings.Cut(kv, "="); ok {
			if _, replaced := over[key]; replaced {
				continue
			}
		}
		out = append(out, kv)
	}
	keys := make([]string, 0, len(over))
	for k := range over {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+over[k])
	}
	return out
}

// addressBook is the port every service of one file runs on, worked out once
// per groups.start and only for the services this start needs: the ones it
// spawns and the ones their prepare, cmd and env refer to. It is what ${port} and
// ${<service>.port} expand to, what PORT is set to, and what a dependent waits
// for. worktree is the stable identity ${worktree} expands to for the checkout
// holding the manifest.
type addressBook struct {
	ctx      context.Context
	rt       *daemon.Runtime
	cfg      *groups.Config
	group    string
	worktree string
	live     map[string]int
	ports    map[string]int
	errs     map[string]error
	// groups is the one runtime snapshot this start resolves workspace
	// references against. It is read-only: a reference never allocates a port
	// or starts the other checkout's service.
	groups []state.Group
}

func newAddressBook(ctx context.Context, rt *daemon.Runtime, cfg *groups.Config, group string) (*addressBook, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snap, err := snapshotContext(ctx, rt)
	if err != nil {
		return nil, err
	}
	b := &addressBook{
		ctx: ctx, rt: rt, cfg: cfg, group: group,
		worktree: groups.WorktreeTokenFor(cfg.Dir),
		live:     map[string]int{}, ports: map[string]int{}, errs: map[string]error{},
		groups: snap.Groups,
	}
	// A service that is already up keeps the port it is on: the run registry
	// knows the port option-berth started it on, and the group row knows where
	// anything else is listening.
	for _, rec := range runsreg.Default.List() {
		if rec.Group == group && rec.PortHint > 0 {
			b.live[rec.Name] = rec.PortHint
		}
	}
	for _, g := range snap.Groups {
		if g.Name != group {
			continue
		}
		for _, row := range g.Services {
			if _, known := b.live[row.Name]; !known && row.Running && row.PortActual != nil {
				b.live[row.Name] = *row.PortActual
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return b, nil
}

// port is the port a service runs on, or 0 for one that declares none. A fixed
// port is itself. A `port: auto` service that is already up keeps its port;
// one that is not gets its claim.
func (b *addressBook) port(name string) (int, error) {
	if port, ok := b.ports[name]; ok {
		return port, nil
	}
	if err, ok := b.errs[name]; ok {
		return 0, err
	}
	svc, ok := b.cfg.ServiceNamed(name)
	var (
		port int
		err  error
	)
	switch {
	case ok && svc.HasPort():
		switch {
		case svc.Port != 0:
			port = svc.Port
		case b.live[name] != 0:
			port = b.live[name]
		default:
			port, err = daemon.AcquireServicePort(b.rt, b.cfg.Dir, name)
		}
	case !ok:
		// Not one of this file's services: a machine reference is a fixed
		// port, and a workspace reference reads the other checkout's address.
		// Neither is allocated or started here.
		if machine, found := b.cfg.MachineNamed(name); found {
			port = machine.Port
		} else if _, found := b.cfg.WorkspaceNamed(name); found {
			port, err = b.cfg.WorkspaceServiceAddress(name, b.groups)
		}
	}
	if err != nil {
		b.errs[name] = err
		return 0, err
	}
	b.ports[name] = port
	return port, nil
}

// portContext checks ownership around synchronous allocation without pretending
// an in-flight filesystem/allocator call can be forcibly interrupted. Any claim
// already saved remains a valid durable claim, not a rolled-back side effect.
func (b *addressBook) portContext(ctx context.Context, name string) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	port, err := b.port(name)
	if cancelled := ctx.Err(); cancelled != nil {
		return 0, cancelled
	}
	return port, err
}

// forService resolves the ports a service's prepare, cmd and env need: its
// own, and every service they refer to.
func (b *addressBook) forService(svc groups.Service) (map[string]int, error) {
	names := groups.Refs(svc.Prepare, svc.Name)
	names = append(names, groups.Refs(svc.Cmd, svc.Name)...)
	for _, v := range svc.Env {
		names = append(names, groups.Refs(v, svc.Name)...)
	}
	if svc.HasPort() {
		names = append(names, svc.Name)
	}
	out := make(map[string]int, len(names))
	for _, name := range names {
		port, err := b.portContext(b.ctx, name)
		if err != nil {
			return nil, fmt.Errorf("no port for %s: %s", name, detail(err))
		}
		out[name] = port
	}
	return out, nil
}

// alreadyRunning reports whether a service is up, and why we think so.
//
// The run registry is asked first, because it knows the instant a service has
// been spawned while the scanner only knows a second or two later: two
// `oberth up` runs in quick succession must not start the same service twice.
// The group's resolved state answers for everything option-berth did not start — it
// already joins declared ports, run names and display names against what is
// listening.
func alreadyRunning(ctx context.Context, rt *daemon.Runtime, group string, svc groups.Service) (reason, runID string, up bool, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", false, err
	}
	// List is oldest first; the newest matching run is the one the CLI will
	// later read, so name that one rather than the first.
	var newest *runsreg.Record
	list := runsreg.Default.List()
	for i := range list {
		if list[i].Group == group && list[i].Name == svc.Name {
			newest = &list[i]
		}
	}
	if newest != nil {
		return fmt.Sprintf("already started by option-berth (pid %d)", newest.PID), newest.ID, true, ctx.Err()
	}
	snap, err := snapshotContext(ctx, rt)
	if err != nil {
		return "", "", false, err
	}
	for _, g := range snap.Groups {
		if g.Name != group {
			continue
		}
		for _, row := range g.Services {
			if row.Name != svc.Name || !row.Running {
				continue
			}
			if row.RunID != nil {
				runID = *row.RunID
			}
			if row.PortActual != nil {
				return fmt.Sprintf("already running on port %d", *row.PortActual), runID, true, nil
			}
			return "already running", runID, true, nil
		}
	}
	return "", "", false, nil
}

// waitFor blocks until every dependency is ready, or gives up after
// DependencyTimeout. Waiting on the daemon's own state rather than on a socket
// dial is deliberate: the thing that decides a service is up has to be the
// thing every client reads. A no-port dependency uses the run registry's live
// record because it has no listener to wait for.
func waitFor(ctx context.Context, rt *daemon.Runtime, group string, deps []groups.Service, book *addressBook, startID string) error {
	if len(deps) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// One budget belongs to the whole wait, including observation and queueing,
	// not just the gap between polls. A dependency timeout must not cancel the
	// parent plan: independent services still get their own chance to start.
	budget := dependencyTimeout
	waitCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	missing := make([]pendingDependency, len(deps))
	for i, dep := range deps {
		missing[i] = pendingDependency{Name: dep.Name, Display: dep.Name}
	}
	expired := func() error {
		// Preserve parent cancellation/deadline instead of recording it as a
		// dependency failure. For our own deadline retain the last completed
		// missing list (or the dependency names before the first observation).
		if err := ctx.Err(); err != nil {
			return err
		}
		displays := make([]string, 0, len(missing))
		names := make([]string, 0, len(missing))
		for _, pending := range missing {
			displays = append(displays, pending.Display)
			// A wait can be blocked by something that is not a service (an
			// unreadable snapshot); there is no log to name for that.
			if pending.Name != "" {
				names = append(names, pending.Name)
			}
		}
		return dependencyFailure{
			message: fmt.Sprintf("timed out after %s waiting for %s", budget, strings.Join(displays, ", ")),
			names:   names,
		}
	}
	want := make(map[string]int, len(deps))
	for _, dep := range deps {
		if waitCtx.Err() != nil {
			return expired()
		}
		if !dep.HasPort() {
			want[dep.Name] = 0
			continue
		}
		port, err := book.portContext(waitCtx, dep.Name)
		// Port allocation still has synchronous IO. Charge its elapsed time,
		// but do not pretend context can interrupt an uncooperative call.
		if waitCtx.Err() != nil {
			return expired()
		}
		if err != nil {
			return dependencyFailure{
				message: fmt.Sprintf("no port for dependency %s: %s", dep.Name, detail(err)),
				names:   []string{dep.Name},
			}
		}
		want[dep.Name] = port
	}
	// Reuse one timer, armed only after an unsuccessful observation. This
	// retains the existing post-observation poll spacing, unlike a ticker.
	var poll *time.Timer
	defer func() {
		if poll != nil {
			poll.Stop()
		}
	}()
	for {
		if waitCtx.Err() != nil {
			return expired()
		}
		observed := pendingContext(waitCtx, rt, group, deps, want)
		if waitCtx.Err() != nil {
			return expired()
		}
		if len(observed) == 0 {
			return nil
		}
		// A dependency this same groups.start already attempted and that has
		// since exited cannot become ready; waiting out the budget would only
		// turn a known failure into dependency_timeout.
		for _, dep := range observed {
			if dep.Name == "" {
				continue
			}
			if exit, ok := dependencyExited(rt, group, dep.Name, startID); ok {
				return dependencyFailure{
					message: fmt.Sprintf("dependency %s exited with code %d (%s)",
						dep.Name, exit.Code, exit.Reason),
					names: []string{dep.Name},
				}
			}
		}
		missing = observed
		if poll == nil {
			poll = time.NewTimer(dependencyPoll)
		} else {
			poll.Reset(dependencyPoll)
		}
		select {
		case <-waitCtx.Done():
			return expired()
		case <-poll.C:
		}
	}
}

// dependencyExitRegistry is the part of the run registry the dependency wait
// needs. daemon.RunRegistry stays small; a runtime whose registry cannot answer
// both questions keeps the original timeout behavior.
type dependencyExitRegistry interface {
	LatestExit(group, service string) (runsreg.Exit, bool)
	Live(group, service string) bool
}

// dependencyExited reports an exit that proves this wait cannot succeed: the
// dependency was attempted by this same groups.start (its exit carries this
// call's StartID) and no run of it is live now. An older exit, or a live run
// that has not begun listening yet, is not evidence of failure.
func dependencyExited(rt *daemon.Runtime, group, service, startID string) (state.ServiceExit, bool) {
	if rt == nil || startID == "" {
		return state.ServiceExit{}, false
	}
	reg, ok := rt.Runs().(dependencyExitRegistry)
	if !ok || reg.Live(group, service) {
		return state.ServiceExit{}, false
	}
	exit, ok := reg.LatestExit(group, service)
	if !ok || exit.StartID != startID {
		return state.ServiceExit{}, false
	}
	return exit.ServiceExit(), true
}

// pending lists the dependencies that are not ready yet.
func pending(rt *daemon.Runtime, group string, deps []groups.Service, want map[string]int) []pendingDependency {
	return pendingContext(context.Background(), rt, group, deps, want)
}

// pendingDependency is one dependency that has not become ready. Display is what
// a reader sees in the timeout message; Name is the service whose log explains
// it, empty when the wait is blocked by something that is not a service.
type pendingDependency struct {
	Name    string
	Display string
}

func pendingContext(ctx context.Context, rt *daemon.Runtime, group string, deps []groups.Service, want map[string]int) []pendingDependency {
	snap, err := rt.Scanner.SnapshotForReadinessContext(ctx)
	if err != nil {
		return []pendingDependency{{Display: "fresh dependency state: " + err.Error()}}
	}
	var missing []pendingDependency
	for _, dep := range deps {
		if dep.HasPort() {
			if !listening(snap, group, dep, want[dep.Name]) {
				missing = append(missing, pendingDependency{Name: dep.Name, Display: fmt.Sprintf("%s on port %d", dep.Name, want[dep.Name])})
			} else if dep.Health != "" && !healthy(snap, group, dep.Name) {
				missing = append(missing, pendingDependency{Name: dep.Name, Display: fmt.Sprintf("%s health check", dep.Name)})
			}
			continue
		}
		if !liveService(rt, snap, group, dep.Name) {
			missing = append(missing, pendingDependency{Name: dep.Name, Display: fmt.Sprintf("%s to be running (no port)", dep.Name)})
			continue
		}
		// A portless worker can still declare a command check (for example a
		// queue consumer that reports readiness only once it has connected).
		// Dependents wait for that verdict too.
		if dep.Health != "" && !healthy(snap, group, dep.Name) {
			missing = append(missing, pendingDependency{Name: dep.Name, Display: fmt.Sprintf("%s health check", dep.Name)})
		}
	}
	return missing
}

// healthy reports the configured health verdict for a dependency. A listener
// is not enough when the manifest explicitly names a health path: the
// dependent must wait for the same service-level readiness fact that `up
// --wait` and `status` expose.
func healthy(snap state.Snapshot, group, service string) bool {
	for _, g := range snap.Groups {
		if g.Name != group {
			continue
		}
		for _, row := range g.Services {
			if row.Name != service {
				continue
			}
			return row.HealthStatus != nil && row.HealthStatus.Status == state.HealthOK
		}
	}
	return false
}

// liveService checks the run registry directly before consulting the cached
// group row. The direct check closes the scanner's refresh gap after a worker
// is spawned; the row fallback keeps the wait useful with a registry that only
// exposes the resolver-facing state.
func liveService(rt *daemon.Runtime, snap state.Snapshot, group, service string) bool {
	reg := rt.Runs()
	if live, ok := reg.(groups.Liveness); ok {
		reg.Prune()
		return live.Live(group, service)
	}
	for _, g := range snap.Groups {
		if g.Name != group {
			continue
		}
		for _, row := range g.Services {
			if row.Name == service {
				return row.Running
			}
		}
	}
	return false
}

// listening checks declared service facts before any raw-listener fallback.
// A registered run is not readiness for a ported dependency: it must have an
// observed listener. An existing service row is authoritative, including false;
// a raw port must not override generation filtering performed by servicefacts.
// Only when the row is absent may an explicitly attributed local listener match.
// Machine references are not service dependencies in groups.Plan.
func listening(snap state.Snapshot, group string, dep groups.Service, port int) bool {
	for _, g := range snap.Groups {
		if g.Name != group {
			continue
		}
		for _, row := range g.Services {
			if row.Name == dep.Name {
				return row.Running && row.PortActual != nil && *row.PortActual > 0
			}
		}
	}
	if port <= 0 || group == "" {
		return false
	}
	for _, p := range snap.Ports {
		if p.Port != port || !state.IsLocalhost(p.Host) || p.Group == nil || *p.Group != group {
			continue
		}
		if p.Run != nil &&
			((p.Run.Group != "" && p.Run.Group != group) || (p.Run.Name != "" && p.Run.Name != dep.Name)) {
			continue
		}
		return true
	}
	return false
}

// snapshotContext is a control observation: cancellation and collector failure
// must not turn into an empty/last-good input that authorizes another spawn.
func snapshotContext(ctx context.Context, rt *daemon.Runtime) (state.Snapshot, error) {
	return rt.Scanner.SnapshotForReadinessContext(ctx)
}

// resolveConfig finds the `oberth.yaml` this call is about, by path or by
// group name.
func resolveConfig(rt *daemon.Runtime, p rpc.GroupsStartParams) (*groups.Config, error) {
	if p.ConfigPath != nil && strings.TrimSpace(*p.ConfigPath) != "" {
		path := strings.TrimSpace(*p.ConfigPath)
		if cfg, ok := rt.Scanner.ConfigAt(path); ok {
			return cfg, nil
		}
		if err := rt.Scanner.LoadConfig(path); err != nil {
			var bad *groups.ConfigError
			if errors.As(err, &bad) {
				return nil, rpc.NewError(rpc.CodeInvalidConfig, bad.Error(),
					"fix the file and try again")
			}
			return nil, rpc.NewError(rpc.CodeNotFound, "cannot read "+path+": "+err.Error(),
				"`oberth init` writes a "+groups.ConfigName+" at the repository root")
		}
		if cfg, ok := rt.Scanner.ConfigAt(path); ok {
			return cfg, nil
		}
		return nil, rpc.NewError(rpc.CodeNotFound, "no usable "+groups.ConfigName+" at "+path, "")
	}
	if p.Name != nil && strings.TrimSpace(*p.Name) != "" {
		name := strings.TrimSpace(*p.Name)
		if cfg, ok := rt.Scanner.ConfigNamed(name); ok {
			return cfg, nil
		}
		return nil, rpc.NewError(rpc.CodeNotFound,
			"no project named "+name+" has a "+groups.ConfigName,
			"run `oberth status` inside a worktree with a manifest")
	}
	return nil, rpc.NewError(rpc.CodeInvalidParams, "name or config_path is required",
		`send {"name": "my-app"} or {"config_path": "/repo/oberth.yaml"}`)
}

// detail unwraps an rpc error so a chunk carries the message a user reads
// rather than the JSON-RPC envelope's wrapper.
func detail(err error) string {
	message, _ := detailAndHint(err)
	return message
}

// detailAndHint is detail plus the error's own next step. An error that already
// carries the way out keeps it: replacing that hint with a guess is how a
// refusal to start sent an agent to a log file that was never written.
func detailAndHint(err error) (message, hint string) {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Data.Detail, re.Data.Hint
	}
	return err.Error(), ""
}

// dependencyFailure names the dependencies that kept a service from starting.
// waitFor knows which ones never became ready; the caller must point at one of
// those rather than at the first dependency the manifest happens to list, which
// may be perfectly healthy.
type dependencyFailure struct {
	message string
	names   []string
}

func (e dependencyFailure) Error() string { return e.message }

// dependencyHint names the dependency whose log explains the failure. The
// service that was waiting has not run yet, so its own log does not exist.
func dependencyHint(err error) string {
	var failure dependencyFailure
	if errors.As(err, &failure) && len(failure.names) > 0 {
		return logsHint(failure.names[0])
	}
	return ""
}
