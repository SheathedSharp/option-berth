package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/display"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/spf13/cobra"

	// The daemon serves groups.start from this package's init(); `oberth serve`
	// runs in this binary, so it has to be linked in.
	_ "github.com/sheathedsharp/option-berth/internal/daemon/groupstart"
)

var (
	upOnly             []string
	upJSON             bool
	upAllowOutsideHome bool
	upWait             bool
	upWaitTimeout      = 30 * time.Second
)

var upCmd = &cobra.Command{
	Use:     "up [project]",
	Short:   "Start a project's services from its oberth.yaml",
	GroupID: commandGroupCore,
	Long: "Start every service the project's oberth.yaml declares, in depends_on\n" +
		"order: a service waits for the ports its dependencies declare before it\n" +
		"is started, and services that are already running are skipped.\n\n" +
		"Use --wait (or --ready) when the caller needs listeners and configured\n" +
		"health checks to be ready before the command returns.\n\n" +
		"Each service runs detached in its own process group, with stdout and\n" +
		"stderr in ~/.option-berth/logs/<project>/<service>.log. Stop them all\n" +
		"again with `oberth down`.\n\n" +
		"With no argument the project comes from the oberth.yaml at or above the\n" +
		"current directory.",
	Args: cobra.MaximumNArgs(1),
	RunE: upRun,
}

func init() {
	upCmd.Flags().StringSliceVar(&upOnly, "only", nil, "Start only these services (comma separated)")
	upCmd.Flags().BoolVar(&upJSON, "json", false, "Output as JSON")
	upCmd.Flags().BoolVar(&upAllowOutsideHome, "allow-outside-home", false,
		"Allow services whose worktree is outside the user's home directory")
	upCmd.Flags().BoolVar(&upWait, "wait", false,
		"Wait until started services are listening and configured health checks pass")
	upCmd.Flags().BoolVar(&upWait, "ready", false,
		"Alias for --wait")
	upCmd.Flags().DurationVar(&upWaitTimeout, "wait-timeout", upWaitTimeout,
		"Maximum time to wait for services to become ready (e.g. 30s)")
	rootCmd.AddCommand(upCmd)
}

func upRun(cmd *cobra.Command, args []string) error {
	params, cfg, err := upParams(args)
	if err != nil {
		return err
	}

	c, err := connectForWrite(cmd.Context())
	if err != nil {
		return err
	}
	defer c.Close()

	if err := requireConfigSupport(c, cfg); err != nil {
		return err
	}

	var start rpc.GroupsStartResult
	stream, err := c.Stream(cmd.Context(), "groups.start", params, &start)
	if err != nil {
		return cliError(err)
	}
	defer stream.Close()

	return consumeStartWithWait(cmd.Context(), c, stream, upJSON, upWait, upWaitTimeout, params, cfg)
}

// upParams turns the command line into groups.start params: a name when one was
// given, the config at or above the working directory otherwise.
func upParams(args []string) (rpc.GroupsStartParams, *groups.Config, error) {
	return projectStartParams(args, upOnly, upAllowOutsideHome)
}

func projectStartParams(args []string, only []string, allowOutsideHome bool) (rpc.GroupsStartParams, *groups.Config, error) {
	params := rpc.GroupsStartParams{
		HostParams:       hostParams(),
		Only:             only,
		AllowOutsideHome: allowOutsideHome,
	}
	// The services run as if started from this shell: the toolchain they need is
	// this machine's, and there is no other machine to be confused about.
	params.Env = callerEnv()
	if len(args) == 1 {
		// Named from anywhere: the file is the daemon's to find, so there is
		// nothing here to check against it.
		name := strings.TrimSpace(args[0])
		params.Name = &name
		return params, nil, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return params, nil, fmt.Errorf("resolving the working directory: %w", err)
	}
	index := groups.NewIndex()
	index.Observe(wd)
	cfg := index.Nearest(wd)
	if cfg == nil {
		// A file that is there but broken is a different problem from no file
		// at all, and saying so is the difference between a two-second fix and
		// a puzzled `ls -a`.
		if bad := index.Invalid(); len(bad) > 0 {
			return params, nil, fmt.Errorf("%s cannot be used: %w", groups.ConfigName, bad[0].Err)
		}
		return params, nil, fmt.Errorf("no %s at or above %s\nhint: `oberth init` writes one, or name a project: `oberth up <project>`",
			groups.ConfigName, shortPath(wd))
	}
	params.ConfigPath = &cfg.Path
	return params, cfg, nil
}

// consumeStart prints one line per service as the daemon reports it, then the
// summary. It exits non-zero when any service failed to start (spec, "Error
// handling": a partial failure is still a failure).
func consumeStart(stream *client.Stream, asJSON bool) error {
	return consumeStartWithWait(context.Background(), nil, stream, asJSON, false, 0, rpc.GroupsStartParams{}, nil)
}

func consumeStartWithWait(ctx context.Context, c *client.Client, stream *client.Stream, asJSON, wait bool,
	waitTimeout time.Duration, params rpc.GroupsStartParams, cfg *groups.Config) error {
	var chunks []rpc.GroupsStartChunk

	for chunk := range stream.Chunks() {
		var c rpc.GroupsStartChunk
		if err := json.Unmarshal(chunk, &c); err != nil {
			continue
		}
		chunks = append(chunks, c)
		if !asJSON && !wait {
			printStartChunk(c)
		}
	}

	end := <-stream.End()
	if end.Err != nil {
		return cliError(end.Err)
	}
	var summary rpc.GroupsStartEnd
	if err := end.Decode(&summary); err != nil {
		return err
	}
	for i := range chunks {
		normalizeStartChunk(&chunks[i])
	}
	var cleanupErr error
	if wait {
		timedOut, err := waitForStartReady(ctx, params, cfg, chunks, &summary, waitTimeout)
		if err != nil {
			return err
		}
		if len(timedOut) > 0 && c != nil {
			cleanupErr = stopTimedOutServices(ctx, c, params, timedOut)
		}
	}

	if asJSON {
		if err := printJSON(struct {
			Services []rpc.GroupsStartChunk `json:"services"`
			rpc.GroupsStartEnd
		}{Services: chunks, GroupsStartEnd: summary}); err != nil {
			return err
		}
	} else {
		if wait {
			for _, chunk := range chunks {
				printStartChunk(chunk)
			}
		}
		printStartSummary(summary)
	}
	if len(summary.Errors) > 0 {
		if cleanupErr != nil {
			return cleanupErr
		}
		return errSilent
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	return nil
}

// stopTimedOutServices makes a readiness timeout terminal. The same group
// mutation path used by `oberth down` stops portless workers as well as
// listeners, while Only keeps already-running or skipped services untouched.
func stopTimedOutServices(ctx context.Context, c *client.Client, params rpc.GroupsStartParams, services []string) error {
	var env rpc.KillEnvelope
	err := c.Call(ctx, "groups.kill", rpc.GroupsKillParams{
		HostParams: params.HostParams,
		Name:       valueOrEmpty(params.Name),
		ConfigPath: params.ConfigPath,
		Only:       services,
		Release:    true,
		Reason:     "ready_timeout",
	}, &env)
	if err != nil {
		return cliError(err)
	}
	return nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// normalizeStartChunk keeps a new CLI useful with a daemon that predates the
// structured outcome fields. The daemon remains the source of facts; this is
// only a compatibility projection for the client-side JSON document.
func normalizeStartChunk(c *rpc.GroupsStartChunk) {
	if c.State == "" {
		switch {
		case c.Error != "":
			c.State = "failed"
			c.Reason = classifyStartFailure(c.Error)
		case c.Skipped:
			c.State = "skipped"
		default:
			c.State = "started"
		}
	}
	if c.Error != "" && c.Hint == "" {
		c.Hint = logsHint(c.Service)
	}
}

func classifyStartFailure(message string) string {
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

func logsHint(service string) string { return "oberth logs " + service + " --once" }

func printStartChunk(c rpc.GroupsStartChunk) {
	switch {
	case c.Error != "":
		fmt.Printf("  %s %s  %s\n", display.Red("x"), display.Bold(c.Service), display.Dim(c.Error))
		if c.Hint != "" {
			fmt.Printf("    %s\n", display.Dim("下一步  "+c.Hint))
		}
	case c.Skipped:
		reason := c.Reason
		if reason == "" {
			reason = "already running"
		}
		fmt.Printf("  %s %s  %s\n", display.Dim("-"), display.Bold(c.Service), display.Dim(reason))
	default:
		// The address, not the port number: a terminal makes a URL clickable,
		// and opening the thing you just started is the next thing you do.
		where := display.Dim(fmt.Sprintf("pid %d  %s", c.PID, shortPath(c.LogPath)))
		if c.Port > 0 {
			where = display.Underline(groups.URL(c.Port)) + "  " + where
		}
		fmt.Printf("  %s %s  %s\n", display.Green("✓"), display.Bold(c.Service), where)
	}
}

func printStartSummary(end rpc.GroupsStartEnd) {
	parts := []string{fmt.Sprintf("%d started", len(end.Started))}
	if len(end.Skipped) > 0 {
		parts = append(parts, fmt.Sprintf("%d already running", len(end.Skipped)))
	}
	if len(end.Errors) > 0 {
		parts = append(parts, display.Red(fmt.Sprintf("%d failed", len(end.Errors))))
	}
	fmt.Printf("\n%s\n", display.Dim(strings.Join(parts, ", ")))
}

// shortPath renders a path under the home directory as ~/….
func shortPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || path == "" || !strings.HasPrefix(path, home) {
		return path
	}
	rel, err := filepath.Rel(home, path)
	if err != nil {
		return path
	}
	return filepath.Join("~", rel)
}
