package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/display"
	"github.com/sheathedsharp/option-berth/internal/runs"
	"github.com/sheathedsharp/option-berth/internal/sessions"
	"github.com/sheathedsharp/option-berth/internal/spawn"
	"github.com/sheathedsharp/option-berth/internal/state"
	"github.com/spf13/cobra"

	// The daemon serves runs.register/unregister/list/spawn from this package's
	// init(); `oberth serve` runs in this binary, so it has to be linked in.
	_ "github.com/sheathedsharp/option-berth/internal/daemon/runsreg"
)

var (
	startGroup  string
	startName   string
	startPort   int
	startDetach bool

	// startSession is the agent session this invocation belongs to, detected
	// once in startRun and carried into whichever spawn path runs.
	startSession state.Session
)

// registerTimeout bounds the daemon round-trips around a run. A slow or absent
// daemon must never delay the command the user actually asked for.
const registerTimeout = 5 * time.Second

var startCmd = &cobra.Command{
	Use:     "start [-d] [flags] -- <command> [args...]",
	Short:   "Run one command and record it as a service",
	GroupID: commandGroupSupport,
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
		"Everything after -- is the command, passed through verbatim.",
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
	// The agent session is detected here, in the process the agent actually
	// spawned: the daemon's own environment is a service manager's, not an
	// agent's, so it could never detect this (spec 2 §3).
	session, _ := sessions.Capture(cwd, sessions.Options{})

	startSession = session

	if startDetach {
		return startDetached(cmd, args, cwd, res)
	}
	return startAttached(cmd, args, cwd, res)
}

// startAttached runs the command in the foreground: stdio passes through, the
// child owns its own process group, signals are forwarded to it and option-berth exits
// with the child's code.
func startAttached(cmd *cobra.Command, argv []string, cwd string, res spawn.Resolution) error {
	// Catch the interrupts before the child exists: a `oberth start` line in a
	// dev.sh runs as a background job with SIGINT ignored, and installing a
	// handler here is what gives the child a working Ctrl+C again.
	fwd := spawn.CatchSignals()
	defer fwd.Stop()

	h, err := spawn.Spawn(cmd.Context(), spawn.Request{
		Argv:     argv,
		Cwd:      cwd,
		Group:    res.Group,
		Name:     res.Name,
		PortHint: startPort,
		Session:  startSession,
	})
	if err != nil {
		return err
	}
	fwd.Forward(h)

	daemonKnows := registerRun(h)
	defer unregisterRun(h.PID, daemonKnows)

	code, err := h.Wait()
	if err != nil {
		return fmt.Errorf("running %q: %w", argv[0], err)
	}
	// The daemon keeps how it ended, and knows a Ctrl+C we forwarded is not a
	// crash however the child chose to exit.
	finishRun(h.PID, daemonKnows, code, fwd.Interrupted())
	if code != 0 {
		// Mirror the child's exit code without cobra printing usage over it.
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		fwd.Stop()
		os.Exit(code)
	}
	return nil
}

// startDetached hands the run to the daemon so it is parented by something that
// outlives this shell, falling back to spawning it here when there is no daemon.
func startDetached(cmd *cobra.Command, argv []string, cwd string, res spawn.Resolution) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), registerTimeout)
	defer cancel()

	if c, err := connectDaemon(ctx); err == nil {
		defer c.Close()
		params := rpc.RunsSpawnParams{
			Argv:  argv,
			Cwd:   cwd,
			Env:   callerEnv(),
			Group: &res.Group,
			Name:  &res.Name,
			// The CLI is the user: it may start commands anywhere.
			AllowOutsideHome: true,
		}
		if startSession.ID != "" {
			s := startSession
			params.Session = &s
		}
		if startPort > 0 {
			hint := startPort
			params.PortHint = &hint
		}
		var out rpc.RunsSpawnResult
		if err := c.Call(ctx, "runs.spawn", params, &out); err == nil {
			printStarted(res, out.PID, out.LogPath)
			return nil
		} else if !errors.Is(err, client.ErrNotRunning) {
			return err
		}
	}

	// No daemon: start it here anyway. The run is recorded in runs.json and
	// adopted by the next daemon that starts.
	h, err := spawn.Spawn(cmd.Context(), spawn.Request{
		Argv:     argv,
		Cwd:      cwd,
		Group:    res.Group,
		Name:     res.Name,
		PortHint: startPort,
		Session:  startSession,
		Detach:   true,
	})
	if err != nil {
		return err
	}
	registerRun(h)
	printStarted(res, h.PID, h.LogPath)
	return nil
}

// callerEnv is this process's environment as the map runs.spawn and
// groups.start take, so what the daemon starts runs with the user's PATH,
// toolchain and virtualenv rather than the daemon's own.
func callerEnv() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		// Windows keeps per-drive entries like "=C:=C:\" with an empty key.
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			out[k] = v
		}
	}
	return out
}

func printStarted(res spawn.Resolution, pid int, logPath string) {
	fmt.Printf("%s %s (pid %d)\n",
		display.Dim("started"), display.Cyan(res.Group+"/"+res.Name), pid)
	if logPath != "" {
		fmt.Printf("%s %s\n", display.Dim("logs"), logPath)
	}
}

// registerRun records the run with the daemon, falling back to runs.json when
// there is none. It reports whether the daemon took it.
func registerRun(h *spawn.Handle) bool {
	ctx, cancel := context.WithTimeout(context.Background(), registerTimeout)
	defer cancel()

	c, err := connectDaemon(ctx)
	if err == nil {
		defer c.Close()
		params := rpc.RunsRegisterParams{
			PID:              h.PID,
			PPID:             h.PPID,
			Group:            h.Group,
			Name:             h.Name,
			Cmd:              h.Cmd,
			Cwd:              h.Cwd,
			StartedAt:        h.StartedAt.Format(time.RFC3339),
			ID:               &h.ID,
			AllowOutsideHome: true,
		}
		if h.Session.ID != "" {
			s := h.Session
			params.Session = &s
		}
		if h.PortHint > 0 {
			hint := h.PortHint
			params.PortHint = &hint
		}
		var out rpc.RunsRegisterResult
		if err := c.Call(ctx, "runs.register", params, &out); err == nil {
			return true
		}
	}

	if err := runs.Add(fallbackEntry(h)); err != nil {
		fmt.Fprintf(os.Stderr, "option-berth: warning: could not record this run: %v\n", err)
	}
	return false
}

// unregisterRun removes the run again, wherever it was recorded.
func unregisterRun(pid int, daemonKnows bool) {
	if daemonKnows {
		ctx, cancel := context.WithTimeout(context.Background(), registerTimeout)
		defer cancel()
		if c, err := connectRunningDaemon(ctx); err == nil {
			defer c.Close()
			var out rpc.OKResult
			if err := c.Call(ctx, "runs.unregister", rpc.RunsUnregisterParams{PID: pid}, &out); err == nil {
				return
			}
		}
	}
	_ = runs.Remove(pid)
}

// finishRun reports how an attached run ended, so the daemon keeps it among
// the runs that exited. Without a daemon there is nowhere to keep it and the
// runs.json entry is simply removed.
func finishRun(pid int, daemonKnows bool, code int, stopped bool) {
	if daemonKnows {
		ctx, cancel := context.WithTimeout(context.Background(), registerTimeout)
		defer cancel()
		if c, err := connectRunningDaemon(ctx); err == nil {
			defer c.Close()
			var out rpc.OKResult
			params := rpc.RunsUnregisterParams{PID: pid, ExitCode: &code, Stopped: stopped}
			if err := c.Call(ctx, "runs.unregister", params, &out); err == nil {
				return
			}
		}
	}
	_ = runs.Remove(pid)
}

// fallbackEntry is the runs.json row for a run the daemon never saw. Tag holds
// the group so an older `oberth list` still attributes the ports.
func fallbackEntry(h *spawn.Handle) runs.Entry {
	return runs.Entry{
		PID:       h.PID,
		Tag:       h.Group,
		ID:        h.ID,
		Cmd:       h.Cmd,
		StartedAt: h.StartedAt.Format(time.RFC3339),
		Group:     h.Group,
		Name:      h.Name,
		Cwd:       h.Cwd,
		PPID:      h.PPID,
		PortHint:  h.PortHint,
	}
}

// connectDaemon dials the daemon, starting one if needed: a run has to be
// registered somewhere that outlives the command.
func connectDaemon(ctx context.Context) (*client.Client, error) {
	return client.Connect(ctx, client.ClientInfo{Name: "cli", Version: buildinfo.Version})
}

// connectRunningDaemon dials without autostarting: cleaning up a run is no
// reason to start a daemon.
func connectRunningDaemon(ctx context.Context) (*client.Client, error) {
	return client.Dial(ctx, client.ClientInfo{Name: "cli", Version: buildinfo.Version})
}
