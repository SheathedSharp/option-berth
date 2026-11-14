package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/runs"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/sessions"
	"github.com/sheathedsharp/option-berth/engine/internal/spawn"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/spf13/cobra"

	// The runtime service serves runs.register/unregister/list/spawn from this package's
	// init(); `oberth runtime service run` runs in this binary, so it has to be linked in.
	_ "github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/runsreg"
)

var (
	// What the run is called and where it belongs.
	startGroup string
	startName  string
	startPort  int

	// Whether to hand the run to the runtime service instead of staying in this shell.
	startDetach bool

	// startSession is the agent session this invocation belongs to: detected
	// once in startRun and carried into whichever spawn path runs.
	startSession state.Session
)

// registerTimeout bounds the runtime service round-trips around a run; a slow or absent
// runtime service must never delay the command the user asked for.
const registerTimeout = 5 * time.Second

var startCmd = &cobra.Command{
	Use:     "start [-d] [flags] -- <command> [args...]",
	Short:   "Run one command and record it as a service",
	GroupID: commandGroupOperations,
	Long: "Run one command after -- and record it so option-berth can attribute every\n" +
		"port it (or anything it spawns) opens to a group and a service name.\n\n" +
		"Project services belong to `oberth up`; this is the low-level escape hatch\n" +
		"for a command that is not in the manifest. In the foreground option-berth\n" +
		"passes through its output and Ctrl+C stops the whole process tree. -d starts\n" +
		"it in the background instead.\n\n" +
		"The group is --group, else the nearest oberth.yaml, else the git\n" +
		"checkout the command runs in, else the directory name. The name is\n" +
		"--name, else the matching oberth.yaml service, else inferred from the\n" +
		"command (`npm run dev` is `dev`).\n\n" +
		"The child runs in its own process group with BERTH_GROUP, BERTH_NAME\n" +
		"and BERTH_RUN_ID in its environment. Ctrl+C goes to the whole tree and\n" +
		"option-berth exits with the command's own exit code.\n\n" +
		"Service and workspace references in the command — ${api.url},\n" +
		"${api.port}, ${machine.port}, ${backend.url} and ${worktree} — are expanded\n" +
		"to this worktree's addresses before it runs, including port: auto. A\n" +
		"reference with no address fails instead of running as a literal. Bare\n" +
		"${port} and ${url} expand when --port supplies their value. Everything\n" +
		"else after -- is passed through verbatim.",
	Args:                  cobra.ArbitraryArgs,
	DisableFlagsInUseLine: true,
	RunE:                  startRun,
}

func init() {
	startCmd.Flags().StringVar(&startGroup, "group", "", "Group to attribute this run to (default: oberth.yaml, git root, or directory name)")
	startCmd.Flags().StringVar(&startName, "name", "", "Service name for this run (default: inferred from the command)")
	startCmd.Flags().IntVar(&startPort, "port", 0, "Port this command is expected to bind; the run shows as starting until it does")
	startCmd.Flags().BoolVarP(&startDetach, "detach", "d", false, "Run in the background, logging to ~/.option-berth/logs/<group>/")
	rootCmd.AddCommand(startCmd)
}

func startRun(cmd *cobra.Command, args []string) error {
	if cmd.ArgsLenAtDash() != 0 {
		return usageError{errors.New("start runs one command after `--`; use `oberth up` for manifest services")}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolving the working directory: %w", err)
	}
	if len(args) == 0 {
		return usageError{errors.New("no command given; usage: oberth start [flags] -- <command> [args...]")}
	}
	if startPort < 0 || startPort > 65535 {
		return fmt.Errorf("--port %d is not a port number", startPort)
	}
	res := spawn.Resolve(cwd, args, startGroup, startName)
	// The manifest is resolved by the same pass that named the group and run.
	// The address book is expanded into the command, not into the child's
	// environment: a one-off command keeps the environment the caller gave it.
	argv, err := expandStartAddresses(cmd.Context(), startConfig(res.ConfigPath), args, startPort)
	if err != nil {
		return err
	}
	// The agent session is detected here, in the process the agent actually
	// spawned: the runtime service's own environment is a service manager's, not an
	// agent's, so it could never detect this.
	session, _ := sessions.Capture(cwd, sessions.Options{})

	startSession = session

	if startDetach {
		return startDetached(cmd, argv, cwd, res)
	}
	return startAttached(cmd, argv, cwd, res)
}

// startAttached runs the command in the foreground: stdio passes through, the
// child owns its own process group, signals are forwarded to it and option-berth exits
// with the child's code.
func startAttached(cmd *cobra.Command, argv []string, cwd string, res spawn.Resolution) error {
	// Catch interrupts before the child exists: an `oberth start` line in a
	// dev.sh runs as a background job with SIGINT ignored, so installing the
	// handler here is what gives the child a working Ctrl+C again.
	fwd := spawn.CatchSignals()
	defer fwd.Stop()

	request := spawn.Request{
		PortHint: startPort,
		Cwd:      cwd,
		Argv:     argv,
		Group:    res.Group,
		Name:     res.Name,
		Session:  startSession,
	}
	h, err := spawn.Spawn(cmd.Context(), request)
	if err != nil {
		return err
	}
	fwd.Forward(h)

	inRuntimeService := registerRun(h)
	defer unregisterRun(h.PID, inRuntimeService)

	// The run is recorded in the worktree's active workplace as it happens,
	// with the run identity and log path that make the block checkable later.
	recorder := startRecording(cwd)
	defer recorder.Close()
	blockID := recorder.startShell(argv, h.ID, h.PID, h.LogPath, h.StartedAt)
	exitCode, err := h.Wait()
	if err != nil {
		return fmt.Errorf("running %q: %w", argv[0], err)
	}
	// The runtime service keeps how it ended, and knows a Ctrl+C we forwarded is not a
	// crash however the child chose to exit.
	finishRun(h.PID, inRuntimeService, exitCode, fwd.Interrupted())
	recorder.endShell(blockID, exitCode, fwd.Interrupted())
	// The record is flushed before the exit below, which skips deferred work.
	recorder.Close()
	if exitCode != 0 {
		// Mirror the child's exit code without cobra printing usage over it.
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		fwd.Stop()
		os.Exit(exitCode)
	}
	return nil
}

// startDetached hands the run to the runtime service so it is parented by something that
// outlives this shell, falling back to spawning it here when there is no runtime.service.
//
// The run is recorded in the worktree's workplace, and the record is flushed
// before this returns: this command exits immediately afterwards, so a record
// still in a queue would go with it. The block is left open — the run is still
// going — and the runtime service closes it when it records how the run ended.
func startDetached(cmd *cobra.Command, argv []string, cwd string, res spawn.Resolution) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), registerTimeout)
	defer cancel()

	var out rpc.RunsSpawnResult
	err := runtimeServiceCall(ctx, connectRuntimeService, "runs.spawn", detachedSpawnParams(argv, cwd, res), &out)
	if err == nil {
		printStarted(res, out.PID, out.LogPath)
		printObservedPort(cmd.Context(), out.RunID, out.PID, startPort > 0)
		recordDetachedRun(cwd, argv, out.RunID, out.PID, out.LogPath, time.Now().UTC())
		return nil
	}
	if !errors.Is(err, client.ErrNotRunning) {
		return err
	}

	// No runtime_service: start it here anyway. The run is recorded in runs.json and
	// adopted by the next runtime service that starts.
	request := spawn.Request{
		Detach:   true,
		PortHint: startPort,
		Cwd:      cwd,
		Argv:     argv,
		Group:    res.Group,
		Name:     res.Name,
		Session:  startSession,
	}
	h, err := spawn.Spawn(cmd.Context(), request)
	if err != nil {
		return err
	}
	registerRun(h)
	printStarted(res, h.PID, h.LogPath)
	printObservedPort(cmd.Context(), h.ID, h.PID, startPort > 0)
	recordDetachedRun(cwd, argv, h.ID, h.PID, h.LogPath, h.StartedAt)
	return nil
}

// recordDetachedRun records one detached run and waits for the record to land.
//
// The recorder is created here rather than at the top of startDetached because
// the runtime service this command reaches may have been started by the line above it:
// a recorder that dialled before that would find nothing listening, give up,
// and drop the very record it exists for.
func recordDetachedRun(cwd string, argv []string, runID string, pid int, logPath string, started time.Time) {
	recorder := startRecording(cwd)
	defer recorder.Close()
	recorder.startShell(argv, runID, pid, logPath, started)
}

// callerEnv is this process's environment as the map runs.spawn and
// groups.start take, so what the runtime service starts runs with the user's PATH,
// toolchain and virtualenv rather than the runtime service's own.
func callerEnv() map[string]string {
	env := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		// Windows keeps per-drive entries like "=C:=C:\" with an empty key.
		if found && key != "" {
			env[key] = value
		}
	}
	return env
}

func printStarted(res spawn.Resolution, pid int, logPath string) {
	fmt.Printf("%s %s (pid %d)\n", display.Dim("started"), display.Cyan(res.Group+"/"+res.Name), pid)
	if logPath != "" {
		fmt.Printf("%s %s\n", display.Dim("logs"), logPath)
	}
}

const (
	// detachedPortWait bounds the observation after a detached start. The child
	// binds after the RPC returns, so the port is read from the runtime service's run
	// attribution rather than assumed from --port: the command may bind a
	// fallback port when the hinted one is taken.
	detachedPortWait = 2 * time.Second
	detachedPortPoll = 100 * time.Millisecond
)

// printObservedPort publishes the listeners the runtime service attributes to this run.
// With a --port hint it waits briefly for the attribution; without one it makes
// a single read, because a portless worker has nothing to wait for.
func printObservedPort(ctx context.Context, runID string, pid int, wait bool) {
	if ports := awaitRunPort(ctx, runID, pid, wait); len(ports) > 0 {
		label := "port"
		if len(ports) > 1 {
			label = "ports"
		}
		values := make([]string, len(ports))
		for i, port := range ports {
			values[i] = strconv.Itoa(port)
		}
		fmt.Printf("%s %s\n", display.Dim(label), display.Cyan(strings.Join(values, ", ")))
		return
	}
	if wait {
		fmt.Println(display.Dim("port not observed yet; run `oberth status --json --brief`"))
	}
}

// awaitRunPort polls the run list for a listener attributed to this run. The
// list is read from the runtime service's live registry, not the log, so a fallback port
// is reported as the port the process actually holds.
func awaitRunPort(ctx context.Context, runID string, pid int, wait bool) []int {
	deadline := time.Now()
	if wait {
		deadline = deadline.Add(detachedPortWait)
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		callCtx, cancel := context.WithTimeout(ctx, time.Second)
		var listed rpc.RunsListResult
		err := runtimeServiceCall(callCtx, connectRuntimeService, "runs.list", rpc.Empty{}, &listed)
		cancel()
		if err == nil {
			if ports := portsForRun(listed.Runs, runID, pid); len(ports) > 0 {
				return ports
			}
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(detachedPortPoll):
		}
	}
}

// portsForRun is the identity match: the run id when the caller has one, the
// pid only for a runtime service that did not name the run.
func portsForRun(records []rpc.RunRecord, runID string, pid int) []int {
	for _, rec := range records {
		if runID != "" {
			if rec.ID == runID {
				return rec.Ports
			}
			continue
		}
		if rec.PID == pid {
			return rec.Ports
		}
	}
	return nil
}

// detachedSpawnParams is the runs.spawn request: the caller's command and
// environment, with the resolved group/name. A session or port hint is sent
// only when there is one, because an absent field is the wire's "none".
func detachedSpawnParams(argv []string, cwd string, res spawn.Resolution) rpc.RunsSpawnParams {
	params := rpc.RunsSpawnParams{
		Group: &res.Group,
		Name:  &res.Name,
		Argv:  argv,
		Cwd:   cwd,
		Env:   callerEnv(),
		// The CLI is the user: it may start commands anywhere.
		AllowOutsideHome: true,
	}
	if startSession.ID != "" {
		session := startSession
		params.Session = &session
	}
	if startPort > 0 {
		portHint := startPort
		params.PortHint = &portHint
	}
	return params
}

// registerRun records the run with the runtime service, falling back to runs.json when
// there is none. It reports whether the runtime service took it. Registration is the
// one call allowed to autostart a runtime_service: the record has to outlive this
// command, and a failure of any kind falls back rather than losing the run.
func registerRun(h *spawn.Handle) bool {
	var out rpc.RunsRegisterResult
	err := runtimeServiceCall(context.Background(), connectRuntimeService, "runs.register", registerParams(h), &out)
	if err == nil {
		return true
	}
	if addErr := runs.Add(fallbackEntry(h)); addErr != nil {
		fmt.Fprintf(os.Stderr, "option-berth: warning: could not record this run: %v\n", addErr)
	}
	return false
}

// registerParams is the runs.register request. Like runs.spawn it sends the
// session and port hint only when they carry a value.
func registerParams(h *spawn.Handle) rpc.RunsRegisterParams {
	params := rpc.RunsRegisterParams{
		AllowOutsideHome: true,
		Group:            h.Group,
		Name:             h.Name,
		PID:              h.PID,
		PPID:             h.PPID,
		ID:               &h.ID,
		Cmd:              h.Cmd,
		Cwd:              h.Cwd,
		StartedAt:        h.StartedAt.Format(time.RFC3339),
	}
	if h.Session.ID != "" {
		session := h.Session
		params.Session = &session
	}
	if h.PortHint > 0 {
		portHint := h.PortHint
		params.PortHint = &portHint
	}
	return params
}

// unregisterRun removes the run again, wherever it was recorded.
func unregisterRun(pid int, inRuntimeService bool) {
	endRun(pid, inRuntimeService, rpc.RunsUnregisterParams{PID: pid})
}

// finishRun reports how an attached run ended, so the runtime service keeps it among
// the runs that exited. Without a runtime service there is nowhere to keep it and the
// runs.json entry is simply removed.
func finishRun(pid int, inRuntimeService bool, code int, stopped bool) {
	endRun(pid, inRuntimeService, rpc.RunsUnregisterParams{PID: pid, ExitCode: &code, Stopped: stopped})
}

// endRun removes the run from wherever it was recorded. params carries the
// exit facts only when this call ends a run. Cleanup never autostarts a
// runtime service, and losing the runtime service after registration leaves runs.json as the
// place to remove from.
func endRun(pid int, inRuntimeService bool, params rpc.RunsUnregisterParams) {
	if inRuntimeService {
		var out rpc.OKResult
		if err := runtimeServiceCall(context.Background(), connectRunningRuntimeService, "runs.unregister", params, &out); err == nil {
			return
		}
	}
	_ = runs.Remove(pid)
}

// runtimeServiceCall dials, makes exactly one call, and closes. Each caller chooses
// the dial policy: registration and detached spawn may start a runtime service, while
// cleanup must not. The timeout bounds the whole round trip so a slow runtime service
// cannot delay the command the user asked for.
func runtimeServiceCall(ctx context.Context, dial func(context.Context) (*client.Client, error),
	method string, params, result any) error {
	ctx, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()
	c, err := dial(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Call(ctx, method, params, result)
}

// fallbackEntry is the runs.json row for a run the runtime service never saw. Tag holds
// the group so an older `oberth list` still attributes the ports.
func fallbackEntry(h *spawn.Handle) runs.Entry {
	return runs.Entry{
		ID:        h.ID,
		PID:       h.PID,
		PPID:      h.PPID,
		Tag:       h.Group,
		Group:     h.Group,
		Name:      h.Name,
		Cmd:       h.Cmd,
		Cwd:       h.Cwd,
		PortHint:  h.PortHint,
		StartedAt: h.StartedAt.Format(time.RFC3339),
	}
}

// connectRuntimeService dials the runtime service, starting one if needed: a run has to be
// registered somewhere that outlives the command.
func connectRuntimeService(ctx context.Context) (*client.Client, error) {
	return connectForWrite(ctx)
}

// connectRunningRuntimeService dials without autostarting: cleaning up a run is no
// reason to start a runtime.service.
func connectRunningRuntimeService(ctx context.Context) (*client.Client, error) {
	return client.Dial(ctx, client.ClientInfo{Name: "cli", Version: buildinfo.Version})
}
