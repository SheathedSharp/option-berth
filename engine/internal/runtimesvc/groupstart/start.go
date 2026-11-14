// Package groupstart serves `groups.start`: it walks a `oberth.yaml`'s
// services in dependency order and spawns each one detached, streaming a chunk
// per service as it goes.
//
// It lives outside internal/runtimesvc because starting a service needs the run
// registry, and the runtime service package must not import it. Linking
// this package in is what makes `groups.start` exist; internal/cmd does that
// for the `option-berth` binary.
package groupstart

import (
	"context"
	"errors"
	"fmt"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimecore"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/codeid"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/runsreg"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/engine/internal/spawn"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// DependencyTimeout bounds how long one service waits for something it
// depends_on to become ready. Ported services become ready when they listen;
// no-port services become ready when their run is live. On a timeout the
// dependent service is reported and skipped, and the independent ones still
// start: a slow database must not mean nothing came up.
const DependencyTimeout = 30 * time.Second

// dependencyTimeout is what the wait actually uses. Tests shorten it so a
// timeout is observable without sitting out the real budget.
var dependencyTimeout = DependencyTimeout

// dependencyPoll paces the re-reads of the runtime service's state while the wait for a
// dependency runs. A poll costs nothing until the scanner's cache goes stale.
const dependencyPoll = 250 * time.Millisecond

// startupCheckWindow catches commands that fail before they can become a
// service (missing executable, bind failure, or an immediate exit) without
// delaying healthy services that take a little time to bind their port.
const startupCheckWindow = 100 * time.Millisecond

// errNoCommand is what SplitCmd leaves behind for an empty manifest value.
// Each caller names the field it split, because "no cmd to run" and "empty
// prepare command" are the words the user reads.
var errNoCommand = errors.New("empty command")

func init() {
	runtimesvc.RegisterHandler("groups.start", handleGroupsStart)
	runtimesvc.RegisterCapability("groups")
	runtimesvc.RegisterCapability(rpc.CapabilityStartDependencies)
	runtimesvc.RegisterCapability(rpc.CapabilityStartPhases)
}

func handleGroupsStart(ctx context.Context, req *runtimesvc.Request) (any, error) {
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
	// committed file, never the `name:` the copy carries.
	group := req.Runtime.Scanner.GroupOf(cfg)
	plan, err := startPlan(cfg, p)
	if err != nil {
		return nil, err
	}
	call := &startCall{
		rt:     req.Runtime,
		cfg:    cfg,
		group:  group,
		params: p,
		// One id for everything this call starts, so a client can tell the
		// services it brought up from the ones that were already running.
		meta: runsreg.Meta{ConfigPath: cfg.Path, StartID: spawn.NewID(), Origin: runsreg.Origin(req)},
		end:  rpc.GroupsStartEnd{Started: []string{}, Skipped: []string{}, Errors: []string{}},
	}
	return runtimesvc.StartStream(ctx, req, call.reply(), func(ctx context.Context, s *runtimesvc.Stream) (any, error) {
		return call.serve(ctx, s, plan)
	})
}

// startPlan is the dependency-ordered work this call does, plus the caller's
// errors for naming something the file does not declare.
func startPlan(cfg *groups.Config, p rpc.GroupsStartParams) ([]groups.Step, error) {
	plan, err := groups.PlanWith(cfg, groups.PlanOptions{
		Only:                p.Only,
		IncludeDependencies: p.IncludeDependencies,
	})
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
	return plan, nil
}

// startCall is one `groups.start` invocation. Everything the per-service
// pipeline reads — the runtime, the file, the group, the caller's parameters,
// this call's one start id — is carried here once, so no step has to thread nine
// arguments and none of them can be passed from the wrong call.
//
// The call owns no persisted state: the result it accumulates exists for the
// length of the stream, and the in-flight attempt it publishes is the memory-only
// fact attempts.go owns.
type startCall struct {
	rt     *runtimesvc.Runtime
	cfg    *groups.Config
	group  string
	params rpc.GroupsStartParams
	meta   runsreg.Meta
	end    rpc.GroupsStartEnd

	// ctx and stream are set when the plan starts running; book is the ports
	// table resolved once for the whole call.
	ctx    context.Context
	stream *runtimesvc.Stream
	book   *addressBook
	// changed records that this call registered a run, so the scan cache is
	// invalidated even when the call then failed.
	changed bool
}

// reply is the stream's opening payload: the id naming this call, and the empty
// affected list a start reports (it mutates runs, not groups).
func (c *startCall) reply() rpc.GroupsStartResult {
	return rpc.GroupsStartResult{
		MutationResult: rpc.MutationResult{OK: true, Affected: []string{}},
		StartID:        c.meta.StartID,
	}
}

// serve holds the group for the length of the call, then runs the plan.
func (c *startCall) serve(ctx context.Context, s *runtimesvc.Stream, plan []groups.Step) (any, error) {
	// One start per group at a time. Two concurrent `up` calls would otherwise
	// both pass alreadyRunning before either registers a run, and the loser of
	// the race would report a bind failure for a service that is already
	// coming up.
	release, err := c.rt.AcquireGroup(ctx, c.group)
	if err != nil {
		return nil, err
	}
	defer release()
	return c.run(ctx, s, plan)
}

// run starts the planned services one at a time, sending a chunk per service.
// A service that fails never stops the ones after it: the caller asked for the
// group, and a partial group is more useful than none. Only cancellation and a
// stream that cannot carry a chunk end the plan early.
func (c *startCall) run(ctx context.Context, s *runtimesvc.Stream, plan []groups.Step) (rpc.GroupsStartEnd, error) {
	c.ctx, c.stream = ctx, s
	defer func() {
		// A run registered here is real even when the call then failed, and a
		// recorded failure is a fact the cached snapshot does not have.
		if c.changed || len(c.end.Errors) > 0 {
			c.rt.Scanner.Invalidate()
		}
	}()
	book, err := newAddressBook(ctx, c.rt, c.cfg, c.group)
	if err != nil {
		return c.end, err
	}
	c.book = book

	for _, step := range plan {
		if err := ctx.Err(); err != nil {
			return c.end, err
		}
		if len(step.IncludedBy) > 0 {
			// The summary describes the plan, not the outcome: a dependency
			// that fails still came in through a named root.
			c.end.Included = append(c.end.Included, step.Service.Name)
			sort.Strings(c.end.Included)
		}
		started, err := c.startService(step)
		if started {
			c.changed = true
		}
		if err != nil {
			return c.end, err
		}
	}
	return c.end, nil
}

// startService runs one plan step. It reports whether a run was registered (so
// the caller knows to invalidate the scan) and returns a fatal error only for
// cancellation or a failed stream send; a service failure is recorded in the
// result and does not stop the plan.
//
// The in-flight attempt is created only after the already-running check, so a
// skipped service never owns one. Its finish is deferred, which is what makes
// every exit path — dependency failure, prepare failure, spawn failure,
// cancellation — drop the attempt. On success it is dropped as soon as the run
// is registered: from then on the run record is the only truth.
func (c *startCall) startService(step groups.Step) (bool, error) {
	svc := step.Service
	reason, runID, up, err := alreadyRunning(c.ctx, c.rt, c.group, svc)
	if err != nil {
		return false, err
	}
	if up {
		if err := c.stream.Send(rpc.GroupsStartChunk{
			Service: svc.Name, State: "skipped", Skipped: true, Reason: reason, RunID: runID,
			IncludedBy: step.IncludedBy, ReadyTimeout: svc.EffectiveReadyTimeout().String(),
		}); err != nil {
			return false, err
		}
		c.end.Skipped = append(c.end.Skipped, svc.Name)
		return false, nil
	}

	attempt := beginStartAttempt(c.rt, c.group, c.meta.StartID, svc, dependencyNames(step.Waits), lastPrepareDuration(c.cfg.Path, svc.Name))
	defer attempt.finish()
	if err := sendStartProgress(c.stream, c.params, attempt); err != nil {
		return false, err
	}

	if err := waitForProgress(c.ctx, c.rt, c.group, step.Waits, c.book, c.meta.StartID, attempt.setWaitingOn); err != nil {
		// Losing the request is not evidence that the service failed.
		if err := c.ctx.Err(); err != nil {
			return false, err
		}
		message := detail(err)
		return false, c.reportFailure(svc, step, serviceFailure{
			reason: startFailureReason(message), message: message, hint: dependencyHint(err),
			recordReason: startFailureReason(message), recordMessage: message,
		})
	}

	if strings.TrimSpace(svc.Prepare) != "" {
		proceed, err := c.prepareStep(svc, step, attempt)
		if !proceed {
			return false, err
		}
	}

	attempt.setPhase(phaseSpawning, nil)
	if err := sendStartProgress(c.stream, c.params, attempt); err != nil {
		return false, err
	}
	if err := c.ctx.Err(); err != nil {
		return false, err
	}
	h, err := c.spawnService(svc)
	// A registered run is the new owner of this service's facts. The attempt
	// ends here whether the spawn succeeded or not; the deferred finish is then
	// a no-op, and the run's exit evidence takes over on failure.
	attempt.finish()
	if err != nil {
		if err := c.ctx.Err(); err != nil {
			return false, err
		}
		c.rt.Logger.Warn("starting a service", "group", c.group, "service", svc.Name, "error", err)
		// The hint is the error's own, never a guess. Every failure on this
		// path happened before a process existed, so a "read the logs" hint
		// would name a file that was never written; the refusals that do
		// have a next step carry it themselves.
		message, hint := detailAndHint(err)
		return false, c.reportFailure(svc, step, serviceFailure{
			reason: startFailureReason(message), message: message, hint: hint,
			recordReason: startFailureReason(message), recordMessage: message,
		})
	}

	// A detached run is already registered. Cancelling the observation stops
	// this plan, not that run; its reaper remains the sole exit owner.
	timer := time.NewTimer(startupCheckWindow)
	select {
	case <-c.ctx.Done():
		timer.Stop()
		return true, c.ctx.Err()
	case <-timer.C:
	}
	if err := c.ctx.Err(); err != nil {
		return true, err
	}
	if exit, ok := runsreg.Default.ExitForRun(h.ID, h.PID, h.StartedAt); ok {
		return true, c.reportExit(svc, step, h, exit)
	}
	c.end.Started = append(c.end.Started, svc.Name)
	if err := c.stream.Send(rpc.GroupsStartChunk{
		Service: svc.Name, State: "started", PID: h.PID, Port: h.PortHint, RunID: h.ID,
		LogPath: h.LogPath, LogOffset: h.LogOffset, IncludedBy: step.IncludedBy,
		ReadyTimeout: svc.EffectiveReadyTimeout().String(),
	}); err != nil {
		return true, err
	}
	return true, nil
}

// prepareStep runs the service's optional build step, or skips it when the last
// successful run saw exactly these inputs. It returns whether the service may
// still be spawned, and a fatal error only for cancellation or a broken stream:
// a failed prepare is reported here and the command is never started.
func (c *startCall) prepareStep(svc groups.Service, step groups.Step, attempt *startAttempt) (bool, error) {
	// The skip is an optimization, never a substitute for running the step when
	// the evidence is incomplete. CheckCwd failing here means runPrepare below
	// will report the refusal with its own hint.
	if cwd, err := runsreg.CheckCwd(c.cfg.ServiceDir(svc), c.params.AllowOutsideHome); err == nil &&
		prepareUnchanged(c.cfg.Path, svc, cwd) {
		c.rt.Logger.Info("prepare inputs unchanged; skipping", "group", c.group, "service", svc.Name)
		return true, nil
	}
	attempt.setPhase(phasePrepare, nil)
	if err := sendStartProgress(c.stream, c.params, attempt); err != nil {
		return false, err
	}
	started := time.Now()
	if err := c.runPrepare(svc); err != nil {
		// prepare has already killed and reaped its attached child, so the
		// request being cancelled says nothing about the service.
		if err := c.ctx.Err(); err != nil {
			return false, err
		}
		c.rt.Logger.Warn("preparing a service", "group", c.group, "service", svc.Name, "error", err)
		// Same rule as the cmd path: the error's own hint, or none. A prepare
		// step that failed before its log file was even created has no log to
		// point at.
		message, hint := detailAndHint(err)
		return false, c.reportFailure(svc, step, serviceFailure{
			reason: "start_failed", message: "prepare failed: " + message, hint: hint,
			recordReason: runsreg.ReasonStartFailed, recordMessage: "prepare: " + message,
		})
	}
	elapsed := time.Since(started)
	// The duration is advisory evidence for the next `starting` read; a write
	// failure is not a start failure.
	attempt.setLastPrepare(elapsed.Milliseconds())
	if err := recordPrepareDuration(c.cfg.Path, svc.Name, elapsed); err != nil {
		c.rt.Logger.Warn("recording prepare duration", "group", c.group, "service", svc.Name, "error", err)
	}
	// The step succeeded: remember exactly what it saw. A stamp write failure
	// is not a start failure — the service can still run and the next start
	// simply repeats prepare.
	if cwd, err := runsreg.CheckCwd(c.cfg.ServiceDir(svc), c.params.AllowOutsideHome); err == nil {
		if err := recordPrepareStamp(c.cfg.Path, svc, cwd); err != nil {
			c.rt.Logger.Warn("recording prepare inputs", "group", c.group, "service", svc.Name, "error", err)
		}
	}
	return true, nil
}

// runPrepare runs a service's optional build/code-generation step to completion
// before the long-lived service process is spawned. It is deliberately not a
// registered run: the manifest service remains the one thing `up` starts and
// `down` stops. Preparation output is appended to that service's log so a
// failed build is inspectable with `oberth logs <service> --once`.
func (c *startCall) runPrepare(svc groups.Service) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	argv, ports, err := c.command(svc.Prepare, svc)
	if errors.Is(err, errNoCommand) {
		return fmt.Errorf("service %s has an empty prepare command", svc.Name)
	}
	if err != nil {
		return err
	}
	cwd, err := runsreg.CheckCwd(c.cfg.ServiceDir(svc), c.params.AllowOutsideHome)
	if err != nil {
		return err
	}
	logPath := spawn.LogPath(c.group, svc.Name)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return fmt.Errorf("creating the log directory: %w", err)
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", logPath, err)
	}
	defer log.Close()
	env, err := c.serviceEnv(svc, ports[svc.Name], ports)
	if err != nil {
		return err
	}
	h, err := spawn.Spawn(c.ctx, spawn.Request{
		Argv:   argv,
		Cwd:    cwd,
		Env:    env,
		Group:  c.group,
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
	case <-c.ctx.Done():
		_ = h.Kill()
		<-done
		return c.ctx.Err()
	}
}

// command is what a service's `prepare:` or `cmd:` text becomes: split into
// arguments first, then expanded one argument at a time, so a reference can
// never change how the text divides. It returns the ports those arguments
// mention, which the caller also needs for the environment and the ready check.
func (c *startCall) command(text string, svc groups.Service) ([]string, map[string]int, error) {
	argv := spawn.SplitCmd(text)
	if len(argv) == 0 {
		return nil, nil, errNoCommand
	}
	ports, err := c.book.forService(svc)
	if err != nil {
		return nil, nil, err
	}
	values := groups.Values{Self: svc.Name, Worktree: c.book.worktree, Ports: ports}
	for i := range argv {
		argv[i] = groups.Expand(argv[i], values)
	}
	return argv, ports, nil
}

// spawnService starts one service through the run registry, so the ports it
// opens are attributed to this group and this service name.
func (c *startCall) spawnService(svc groups.Service) (*spawn.Handle, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	argv, ports, err := c.command(svc.Cmd, svc)
	if errors.Is(err, errNoCommand) {
		return nil, fmt.Errorf("service %s has no cmd to run", svc.Name)
	}
	if err != nil {
		return nil, err
	}
	cwd, err := runsreg.CheckCwd(c.cfg.ServiceDir(svc), c.params.AllowOutsideHome)
	if err != nil {
		return nil, err
	}
	port := ports[svc.Name]
	runMeta := c.meta
	runMeta.SpecHash = groups.ServiceSpecHash(svc)
	env, err := c.serviceEnv(svc, port, ports)
	if err != nil {
		return nil, err
	}
	// The run id is chosen before the spawn so the source snapshot can be
	// written first: an immediately exiting process then deletes a file that
	// already exists, never one written after its run is gone.
	runID := spawn.NewID()
	c.captureCodeIdentity(runID, cwd, svc)
	h, err := runsreg.Spawn(c.ctx, c.rt, spawn.Request{
		ID:       runID,
		Argv:     argv,
		Cwd:      cwd,
		Env:      env,
		Group:    c.group,
		Name:     svc.Name,
		PortHint: port,
		LogPath:  spawn.LogPath(c.group, svc.Name),
	}, runMeta)
	if err != nil {
		_ = codeid.Remove(runID)
		return nil, err
	}
	return h, nil
}

// captureCodeIdentity writes this run's source snapshot before the process
// exists. Code identity is a check, not a startup dependency: an unreadable
// source is logged and reported by status as unknown, never rendered as
// "unchanged".
func (c *startCall) captureCodeIdentity(runID, cwd string, svc groups.Service) {
	sources, track := codeid.ResolveSources(svc.Sources)
	if !track {
		return
	}
	snap, err := codeid.Take(c.ctx, cwd, sources)
	if err != nil {
		c.logCodeIdentity(svc.Name, "reading sources", err)
		return
	}
	if err := codeid.Save(runID, snap); err != nil {
		c.logCodeIdentity(svc.Name, "storing source snapshot", err)
	}
}

// logCodeIdentity keeps a failed code check visible without making it a
// service-start failure.
func (c *startCall) logCodeIdentity(service, what string, err error) {
	if c.rt != nil && c.rt.Logger != nil {
		c.rt.Logger.Warn("code identity unavailable", "service", service, "step", what, "error", err)
	}
}

// serviceFailure is one failed step as it is reported. The registry's record and
// the chunk a client reads are not always the same words — a prepare step keeps
// "prepare: ..." while the client reads "prepare failed: ..." — so both are
// carried explicitly instead of being derived twice at the call sites.
type serviceFailure struct {
	reason, message, hint       string
	recordReason, recordMessage string
}

// reportFailure is the one exit for a failed step: it records the failure, wakes
// the observer, and sends the chunk. A stream that cannot carry it is the only
// thing returned, so no path can fail a service without leaving the same trace.
func (c *startCall) reportFailure(svc groups.Service, step groups.Step, f serviceFailure) error {
	recordStartFailureReason(c.cfg, c.group, svc, c.meta, f.recordReason, f.recordMessage)
	c.end.Errors = append(c.end.Errors, svc.Name)
	c.rt.Scanner.Wake()
	return c.stream.Send(rpc.GroupsStartChunk{
		Service: svc.Name, State: "failed", Reason: f.reason,
		Error: f.message, Hint: f.hint, IncludedBy: step.IncludedBy,
	})
}

// reportExit reports a service that exited inside the startup window.
func (c *startCall) reportExit(svc groups.Service, step groups.Step, h *spawn.Handle, exit runsreg.Exit) error {
	c.end.Errors = append(c.end.Errors, svc.Name)
	// A bind failure is explained by whoever still holds the port. This is the
	// first read, so the caller gets the evidence without a second command. A
	// scanner that is absent or cannot observe leaves the holder empty; it must
	// never turn this failure into an internal error.
	var holder *state.PortHolder
	if exit.Reason == runsreg.ReasonPortOccupied && c.rt.Scanner != nil {
		if snap, err := c.rt.Scanner.SnapshotAllContext(c.ctx, scanner.Include{}); err == nil {
			holder = servicefacts.FindPortHolder(snap.Ports, servicefacts.PortHolderQuery{
				Port: exit.PortHint, Group: c.group, Service: svc.Name, RunID: h.ID,
			})
		}
	}
	return c.stream.Send(rpc.GroupsStartChunk{
		Service: svc.Name, State: "failed", Reason: exit.Reason,
		Error: fmt.Sprintf("service exited with code %d (%s)", exit.Code, exit.Reason),
		Hint:  logsHint(svc.Name), PID: h.PID, RunID: h.ID, LogPath: h.LogPath,
		PortHolder: holder, IncludedBy: step.IncludedBy,
	})
}

// dependencyNames is what a `starting` chunk names as the outstanding waits.
func dependencyNames(deps []groups.Service) []string {
	names := make([]string, 0, len(deps))
	for _, dep := range deps {
		names = append(names, dep.Name)
	}
	return names
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

// serviceEnv is the environment a service starts in, each layer winning over
// the one before: the runtime service's own, the caller's (the CLI sends its shell's),
// the service's `env_file`, PORT for a service with a port, and the service's
// own `env:` with its references expanded. BERTH_PORT and the other run
// variables are added by spawn on top of all of it.
//
// The env file is read at start time, so the same committed manifest can point
// every worktree at one file outside their checkouts. Its values are data: they
// are not reference-expanded and never appear in status or logs.
func (c *startCall) serviceEnv(svc groups.Service, port int, ports map[string]int) ([]string, error) {
	env := newEnvSet(os.Environ())
	env.apply(c.params.Env)
	if path := c.cfg.EnvFilePath(svc); path != "" {
		fromFile, err := groups.EnvFileValues(path)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", svc.Name, err)
		}
		env.apply(fromFile)
	}
	if port > 0 {
		env.set("PORT", strconv.Itoa(port))
	}
	values := groups.Values{Self: svc.Name, Worktree: c.book.worktree, Ports: ports}
	declared := make(map[string]string, len(svc.Env))
	for key, value := range svc.Env {
		declared[key] = groups.Expand(value, values)
	}
	env.apply(declared)
	return env.render(), nil
}

// envSet is one process's environment under construction: the inherited
// variables plus the overrides applied so far, in precedence order. Rendering
// keeps the inherited order and appends the overrides sorted by key, so the same
// input always produces the same bytes.
type envSet struct {
	base []string
	over map[string]string
}

func newEnvSet(base []string) *envSet {
	return &envSet{base: base, over: make(map[string]string)}
}

// apply layers every variable in vars on top of what is already set.
func (e *envSet) apply(vars map[string]string) {
	for key, value := range vars {
		e.over[key] = value
	}
}

func (e *envSet) set(key, value string) { e.over[key] = value }

func (e *envSet) render() []string {
	out := make([]string, 0, len(e.base)+len(e.over))
	for _, kv := range e.base {
		if key, _, ok := strings.Cut(kv, "="); ok {
			if _, replaced := e.over[key]; replaced {
				continue
			}
		}
		out = append(out, kv)
	}
	keys := make([]string, 0, len(e.over))
	for key := range e.over {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+"="+e.over[key])
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
	rt       *runtimesvc.Runtime
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

func newAddressBook(ctx context.Context, rt *runtimesvc.Runtime, cfg *groups.Config, group string) (*addressBook, error) {
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
	for _, row := range groupRows(snap, group) {
		if _, known := b.live[row.Name]; !known && row.Running && row.PortActual != nil {
			b.live[row.Name] = *row.PortActual
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return b, nil
}

// port is the port a service runs on, or 0 for one that declares none. A fixed
// port is itself. A `port: auto` service that is already up keeps its port;
// one that is not gets its claim. Both answers are memoized, including the
// error, so one start asks the allocator once per name.
func (b *addressBook) port(name string) (int, error) {
	if port, known := b.ports[name]; known {
		return port, nil
	}
	if err, failed := b.errs[name]; failed {
		return 0, err
	}
	port, err := b.resolve(name)
	if err != nil {
		b.errs[name] = err
		return 0, err
	}
	b.ports[name] = port
	return port, nil
}

// resolve names where a port comes from, in the order the manifest decides it:
// this file's own declaration, a name that is only a machine reference, or a
// workspace reference read from the other checkout's published facts.
func (b *addressBook) resolve(name string) (int, error) {
	svc, declared := b.cfg.ServiceNamed(name)
	if !declared {
		return b.referencePort(name)
	}
	if !svc.HasPort() {
		// A declared service without a port has nothing to expand or wait for.
		return 0, nil
	}
	switch {
	case svc.Port != 0:
		return svc.Port, nil
	case b.live[name] != 0:
		return b.live[name], nil
	default:
		return runtimesvc.AcquireServicePort(b.rt, b.cfg.Dir, name)
	}
}

// referencePort resolves a name that is not one of this file's services. A
// machine reference is a fixed port; a workspace reference reads the other
// checkout's address. Neither is allocated or started here.
func (b *addressBook) referencePort(name string) (int, error) {
	if machine, found := b.cfg.MachineNamed(name); found {
		return machine.Port, nil
	}
	if _, found := b.cfg.WorkspaceNamed(name); found {
		return b.cfg.WorkspaceServiceAddress(name, b.groups)
	}
	return 0, nil
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
	names := serviceRefs(svc)
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

// serviceRefs is every name a service mentions in `prepare:`, `cmd:` and
// `env:`, plus its own when it declares a port.
func serviceRefs(svc groups.Service) []string {
	names := groups.Refs(svc.Prepare, svc.Name)
	names = append(names, groups.Refs(svc.Cmd, svc.Name)...)
	for _, value := range svc.Env {
		names = append(names, groups.Refs(value, svc.Name)...)
	}
	if svc.HasPort() {
		names = append(names, svc.Name)
	}
	return names
}

// alreadyRunning reports whether a service is up, and why we think so.
//
// The run registry is asked first, because it knows the instant a service has
// been spawned while the scanner only knows a second or two later: two
// `oberth up` runs in quick succession must not start the same service twice.
// The question is asked with Running, not List: a run that has already been
// told to stop stays in the registry until its reap, and a restart's start
// half must not read the process it just stopped as one that is still up.
// The group's resolved state answers for everything option-berth did not start — it
// already joins declared ports, run names and display names against what is
// listening.
func alreadyRunning(ctx context.Context, rt *runtimesvc.Runtime, group string, svc groups.Service) (reason, runID string, up bool, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", false, err
	}
	if newest, found := runsreg.Default.Running(group, svc.Name); found {
		return fmt.Sprintf("already started by option-berth (pid %d)", newest.PID), newest.ID, true, ctx.Err()
	}
	snap, err := snapshotContext(ctx, rt)
	if err != nil {
		return "", "", false, err
	}
	row, found := serviceRow(snap, group, svc.Name)
	if !found || !row.Running {
		return "", "", false, nil
	}
	// The resolved row keeps a signalled run visible until its exit is
	// observed, which is the right answer for status and the wrong one here:
	// the registry has already ruled that run out, and this row must not
	// overrule it. What is left is a listener option-berth did not start.
	if runsreg.Default.StopPending(group, svc.Name) {
		return "", "", false, nil
	}
	if row.RunID != nil {
		runID = *row.RunID
	}
	if row.PortActual != nil {
		return fmt.Sprintf("already running on port %d", *row.PortActual), runID, true, nil
	}
	return "already running", runID, true, nil
}

// serviceRow is the one place a snapshot's row for one service is looked up.
// Every readiness question here is about a single service of a single group, so
// the two-level scan lives once instead of in each of them.
func serviceRow(snap state.Snapshot, group, service string) (state.Service, bool) {
	for _, g := range snap.Groups {
		if g.Name != group {
			continue
		}
		for _, row := range g.Services {
			if row.Name == service {
				return row, true
			}
		}
	}
	return state.Service{}, false
}

// groupRows is every service row one group publishes, in the snapshot's order.
func groupRows(snap state.Snapshot, group string) []state.Service {
	for _, g := range snap.Groups {
		if g.Name == group {
			return g.Services
		}
	}
	return nil
}

// waitFor blocks until every dependency is ready, or gives up after
// DependencyTimeout. Waiting on the runtime service's own state rather than on a socket
// dial is deliberate: the thing that decides a service is up has to be the
// thing every client reads. A no-port dependency uses the run registry's live
// record because it has no listener to wait for.
//
// It is waitForProgress without a progress callback, kept so existing callers
// and tests read the same as before.
func waitFor(ctx context.Context, rt *runtimesvc.Runtime, group string, deps []groups.Service, book *addressBook, startID string) error {
	return waitForProgress(ctx, rt, group, deps, book, startID, nil)
}

// waitForProgress is waitFor with an observer: every completed dependency poll
// reports the dependencies still outstanding, so a start attempt can publish a
// waiting set that shrinks as things come up instead of the declaration it
// began with.
//
// onPending receives the still-waiting names in declaration order, an empty
// set once everything is ready, and nothing at all when the observation itself
// failed. The last case matters: "the fresh dependency state could not be
// read" is not evidence that nothing is being waited on, and rendering that
// unknown as an empty set would claim the opposite of what happened.
func waitForProgress(ctx context.Context, rt *runtimesvc.Runtime, group string, deps []groups.Service, book *addressBook, startID string, onPending func([]string)) error {
	if len(deps) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// One budget belongs to the whole wait, including observation and queueing,
	// not just the gap between polls. It is the largest effective readiness
	// budget among the dependencies, so a dependent waits as long as the
	// slowest thing it declared lets it. A dependency with no ready_timeout
	// keeps dependencyTimeout, the package's test seam. A dependency timeout
	// must not cancel the parent plan: independent services still get their
	// own chance to start.
	budget := dependencyBudget(deps)
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
		if onPending != nil {
			reportPending(observed, onPending)
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

// dependencyBudget is the longest readiness budget among deps. A dependency
// that declares ready_timeout is waited for on its own budget; one that does
// not keeps dependencyTimeout so the existing test seam still governs it.
func dependencyBudget(deps []groups.Service) time.Duration {
	budget := time.Duration(0)
	for _, dep := range deps {
		d := dependencyTimeout
		if strings.TrimSpace(dep.ReadyTimeout) != "" {
			d = dep.EffectiveReadyTimeout()
		}
		if d > budget {
			budget = d
		}
	}
	return budget
}

// dependencyExitRegistry is the part of the run registry the dependency wait
// needs. runtime.service.RunRegistry stays small; a runtime whose registry cannot answer
// both questions keeps the original timeout behavior.
type dependencyExitRegistry interface {
	LatestExit(group, service string) (runsreg.Exit, bool)
	Live(group, service string) bool
}

// dependencyExited reports an exit that proves this wait cannot succeed: the
// dependency was attempted by this same groups.start (its exit carries this
// call's StartID) and no run of it is live now. An older exit, or a live run
// that has not begun listening yet, is not evidence of failure.
func dependencyExited(rt *runtimesvc.Runtime, group, service, startID string) (state.ServiceExit, bool) {
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
func pending(rt *runtimesvc.Runtime, group string, deps []groups.Service, want map[string]int) []pendingDependency {
	return pendingContext(context.Background(), rt, group, deps, want)
}

// pendingDependency is one dependency that has not become ready. Display is what
// a reader sees in the timeout message; Name is the service whose log explains
// it, empty when the wait is blocked by something that is not a service.
type pendingDependency struct {
	Name    string
	Display string
}

// reportPending turns one observation into the set a reader should see: the
// still-waiting dependency names in declaration order, nil when everything is
// ready, and no call at all when the observation failed.
//
// An observation that failed is reported as exactly one entry with no name
// (there is no service whose log explains an unreadable snapshot). The caller's
// last known set stays published: "unknown" must not be rendered as "waiting
// for nothing", which is what an empty set says.
func reportPending(observed []pendingDependency, onPending func([]string)) {
	if len(observed) == 0 {
		onPending(nil)
		return
	}
	names := make([]string, 0, len(observed))
	for _, pending := range observed {
		if pending.Name != "" {
			names = append(names, pending.Name)
		}
	}
	if len(names) == 0 {
		return
	}
	onPending(names)
}

func pendingContext(ctx context.Context, rt *runtimesvc.Runtime, group string, deps []groups.Service, want map[string]int) []pendingDependency {
	needsObservation := false
	for _, dep := range deps {
		if dep.HasPort() || dep.Health != "" {
			needsObservation = true
			break
		}
	}
	var snap state.Snapshot
	var err error
	if !needsObservation {
		var have bool
		snap, have, err = rt.Scanner.RefreshRuntimeContext(ctx)
		if err == nil && !have {
			snap, err = rt.Scanner.SnapshotForReadinessContext(ctx)
		}
	} else {
		snap, err = rt.Scanner.SnapshotForReadinessContext(ctx)
	}
	if err != nil {
		return []pendingDependency{{Display: "fresh dependency state: " + err.Error()}}
	}
	var missing []pendingDependency
	for _, dep := range deps {
		if row, found := serviceRow(snap, group, dep.Name); found {
			// The same pure readiness verdict drives CLI waits and dependencies.
			ready, reason := runtimecore.Ready(state.Group{Name: group, Services: []state.Service{row}}, snap.Ports, dep.Name)
			if !ready {
				missing = append(missing, pendingDependency{Name: dep.Name, Display: dependencyPendingDisplay(dep.Name, reason, dep.HasPort())})
			}
			continue
		}
		if dep.HasPort() {
			if !listening(snap, group, dep, want[dep.Name]) {
				missing = append(missing, pendingDependency{Name: dep.Name, Display: fmt.Sprintf("%s on port %d", dep.Name, want[dep.Name])})
			} else if dep.Health != "" && !healthy(snap, group, dep.Name) {
				missing = append(missing, pendingDependency{Name: dep.Name, Display: fmt.Sprintf("%s health check", dep.Name)})
			}
			continue
		}
		if !liveService(rt, snap, group, dep.Name) {
			missing = append(missing, pendingDependency{Name: dep.Name, Display: dependencyPendingDisplay(dep.Name, "not_running", false)})
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
	row, found := serviceRow(snap, group, service)
	return found && row.HealthStatus != nil && row.HealthStatus.Status == state.HealthOK
}

// liveService checks the run registry directly before consulting the cached
// group row. The direct check closes the scanner's refresh gap after a worker
// is spawned; the row fallback keeps the wait useful with a registry that only
// exposes the resolver-facing state.
func liveService(rt *runtimesvc.Runtime, snap state.Snapshot, group, service string) bool {
	reg := rt.Runs()
	if live, ok := reg.(groups.Liveness); ok {
		reg.Prune()
		return live.Live(group, service)
	}
	row, found := serviceRow(snap, group, service)
	return found && row.Running
}

// listening checks declared service facts before any raw-listener fallback.
// A registered run is not readiness for a ported dependency: it must have an
// observed listener. An existing service row is authoritative, including false;
// a raw port must not override generation filtering performed by servicefacts.
// Only when the row is absent may an explicitly attributed local listener match.
// Machine references are not service dependencies in groups.Plan.
func listening(snap state.Snapshot, group string, dep groups.Service, port int) bool {
	if row, found := serviceRow(snap, group, dep.Name); found {
		return row.Running && row.PortActual != nil && *row.PortActual > 0
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
func snapshotContext(ctx context.Context, rt *runtimesvc.Runtime) (state.Snapshot, error) {
	return rt.Scanner.SnapshotForReadinessContext(ctx)
}

// resolveConfig finds the `oberth.yaml` this call is about, by path or by
// group name.
func resolveConfig(rt *runtimesvc.Runtime, p rpc.GroupsStartParams) (*groups.Config, error) {
	if path := trimmed(p.ConfigPath); path != "" {
		return configAtPath(rt, path)
	}
	if name := trimmed(p.Name); name != "" {
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

// configAtPath answers with the file at path, loading it when the scanner has
// not seen it yet. A file that cannot be read is the caller's path to fix; a
// file that reads but declares nothing usable is a different answer.
func configAtPath(rt *runtimesvc.Runtime, path string) (*groups.Config, error) {
	cfg, known := rt.Scanner.ConfigAt(path)
	if !known {
		if err := rt.Scanner.LoadConfig(path); err != nil {
			return nil, configLoadError(path, err)
		}
		cfg, known = rt.Scanner.ConfigAt(path)
	}
	if !known {
		return nil, rpc.NewError(rpc.CodeNotFound, "no usable "+groups.ConfigName+" at "+path, "")
	}
	return cfg, nil
}

func configLoadError(path string, err error) error {
	var bad *groups.ConfigError
	if errors.As(err, &bad) {
		return rpc.NewError(rpc.CodeInvalidConfig, bad.Error(), "fix the file and try again")
	}
	return rpc.NewError(rpc.CodeNotFound, "cannot read "+path+": "+err.Error(),
		"`oberth init` writes a "+groups.ConfigName+" at the repository root")
}

func trimmed(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
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
