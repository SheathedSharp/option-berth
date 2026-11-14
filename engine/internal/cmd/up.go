package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/spf13/cobra"

	// The daemon serves groups.start from this package's init(); `oberth serve`
	// runs in this binary, so it has to be linked in.
	_ "github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/groupstart"
)

var (
	upOnly             []string
	upJSON             bool
	upAllowOutsideHome bool
	upWait             bool
	upNoDeps           bool
	// upWaitTimeout stays zero unless the caller sets --wait-timeout. Without
	// that flag each service is judged against its own ready_timeout.
	upWaitTimeout time.Duration
)

var upCmd = &cobra.Command{
	Use:     "up [project]",
	Short:   "Start a project's services from its oberth.yaml",
	GroupID: commandGroupOperations,
	Long: "Start every service the project's oberth.yaml declares, in depends_on\n" +
		"order: a service waits for the ports its dependencies declare before it\n" +
		"is started, and services that are already running are skipped.\n\n" +
		"With --only, the named services' transitive depends_on closure is started\n" +
		"too, so the things they need come up with them. Pass --no-deps to start\n" +
		"only the named services themselves.\n\n" +
		"Use --wait (or --ready) when the caller needs listeners and configured\n" +
		"health checks to be ready before the command returns. Without it a\n" +
		"started service is reported as started, not as ready. That difference is\n" +
		"the reason to pass --wait: `up --wait --json` is the one call that answers\n" +
		"whether a service is really up, and a started service with\n" +
		"services[].ready false is unconfirmed, not failed.\n\n" +
		"Each service runs detached in its own process group, with stdout and\n" +
		"stderr in ~/.option-berth/logs/<project>/<service>.log. Stop them all\n" +
		"again with `oberth down`.\n\n" +
		"With no argument the project comes from the oberth.yaml at or above the\n" +
		"current directory.",
	Args: cobra.MaximumNArgs(1),
	RunE: upRun,
}

func init() {
	upCmd.Flags().StringSliceVar(&upOnly, "only", nil, "Start these services and their depends_on closure (comma separated)")
	upCmd.Flags().BoolVar(&upNoDeps, "no-deps", false, "Start only the named services, without their depends_on closure")
	upCmd.Flags().BoolVar(&upJSON, "json", false, "Output as JSON")
	upCmd.Flags().BoolVar(&upAllowOutsideHome, "allow-outside-home", false,
		"Allow services whose worktree is outside the user's home directory")
	upCmd.Flags().BoolVar(&upWait, "wait", false,
		"Wait until selected services, including already-running ones, are ready")
	upCmd.Flags().BoolVar(&upWait, "ready", false,
		"Alias for --wait")
	upCmd.Flags().DurationVar(&upWaitTimeout, "wait-timeout", upWaitTimeout,
		"Maximum total time to wait for services to become ready; shortens but never extends each service's ready_timeout")
	rootCmd.AddCommand(upCmd)
}

func upRun(cmd *cobra.Command, args []string) error {
	// Validate before resolving a project or issuing any mutating RPC.
	mode, err := upWaitModeFromFlags(cmd.Flags().Changed("wait-timeout"), upWait, upWaitTimeout)
	if err != nil {
		return err
	}
	params, cfg, err := upParams(args)
	if err != nil {
		return err
	}

	c, err := connectForWrite(cmd.Context())
	if err != nil {
		return err
	}
	defer c.Close()

	if err := requireStartSupport(cmd.Context(), c, params, cfg); err != nil {
		return err
	}
	// Progress is an enhancement: ask for in-flight chunks only from a daemon
	// that advertises them. An older daemon ignores the field entirely; a CLI
	// that cannot see the capability simply keeps the old stream. A --json
	// invocation never asks: its one JSON value is the whole answer, and a
	// diagnostic line on stderr would break the contract for callers that
	// treat a failed command's stderr as the error shape.
	params.Progress = !upJSON && hasCapability(c.Hello(), rpc.CapabilityStartPhases)

	var start rpc.GroupsStartResult
	stream, err := c.Stream(cmd.Context(), "groups.start", params, &start)
	if err != nil {
		return cliError(err)
	}
	defer stream.Close()

	return consumeStartWithWait(cmd.Context(), c, stream, upJSON, mode.wait, mode.timeout,
		mode.timeoutSet, params, cfg)
}

// requireStartSupport refuses a request the running daemon cannot honour, before
// any of it is sent: the manifest's own features, the file a named project
// resolves to, and the depends_on closure each need a capability a daemon may
// predate. Sending the request anyway would turn a daemon too old for the file
// into an error about the file.
func requireStartSupport(ctx context.Context, c *client.Client, params rpc.GroupsStartParams, cfg *groups.Config) error {
	if err := requireConfigSupport(c, cfg); err != nil {
		return err
	}
	// A named project carries no manifest here, so the same capability rules
	// run against the file the daemon will actually read.
	if err := requireNamedProjectSupport(ctx, c, params); err != nil {
		return err
	}
	return requireStartDependencySupport(c, params.IncludeDependencies)
}

// upWaitMode is the readiness wait this invocation asked for. An explicit
// --wait-timeout is a total ceiling: it must be positive and only makes sense
// together with --wait. Without it each service's own ready_timeout decides.
type upWaitMode struct {
	wait       bool
	timeout    time.Duration
	timeoutSet bool
}

func upWaitModeFromFlags(changed, wait bool, timeout time.Duration) (upWaitMode, error) {
	if changed && timeout <= 0 {
		return upWaitMode{}, usageError{fmt.Errorf("--wait-timeout must be positive")}
	}
	if changed && !wait {
		return upWaitMode{}, usageError{fmt.Errorf("--wait-timeout requires --wait or --ready")}
	}
	return upWaitMode{wait: wait, timeout: timeout, timeoutSet: changed}, nil
}

// upParams turns the command line into groups.start params: a name when one was
// given, the config at or above the working directory otherwise.
func upParams(args []string) (rpc.GroupsStartParams, *groups.Config, error) {
	params, cfg, err := projectStartParams(args, upOnly, upAllowOutsideHome)
	if err != nil {
		return params, cfg, err
	}
	// `up` brings a named service's dependencies with it unless asked not to.
	// Without --only the field cannot change the plan (everything is started
	// anyway), so it is not sent: that keeps a plain `up` working against a
	// daemon that predates the closure and does not announce the capability.
	params.IncludeDependencies = !upNoDeps && namesServices(upOnly)
	return params, cfg, nil
}

// namesServices reports whether --only named at least one non-blank service.
// A blank list is the same as no filter: there is nothing to expand.
func namesServices(only []string) bool {
	for _, name := range only {
		if strings.TrimSpace(name) != "" {
			return true
		}
	}
	return false
}

func projectStartParams(args []string, only []string, allowOutsideHome bool) (rpc.GroupsStartParams, *groups.Config, error) {
	params := rpc.GroupsStartParams{
		Only:             only,
		AllowOutsideHome: allowOutsideHome,
	}
	// Services inherit this shell's environment: the toolchain they need is this
	// machine's, and there is no other machine to be confused about.
	params.Env = callerEnv()
	if len(args) == 1 {
		// A named project is resolved by the daemon, which is the side that
		// reads the file, so nothing here has to check it first.
		named := strings.TrimSpace(args[0])
		params.Name = &named
		return params, nil, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return params, nil, fmt.Errorf("resolving the working directory: %w", err)
	}
	index := groups.NewIndex()
	index.Observe(cwd)
	found := index.Nearest(cwd)
	if found == nil {
		return params, nil, missingConfigError(index, cwd)
	}
	params.ConfigPath = &found.Path
	return params, found, nil
}

// missingConfigError explains why no manifest answered for dir. A file that is
// there but unparseable is a different problem from no file at all, and naming
// the difference is what separates a two-second fix from a puzzled `ls -a`.
func missingConfigError(index *groups.Index, dir string) error {
	if invalid := index.Invalid(); len(invalid) > 0 {
		return fmt.Errorf("%s cannot be used: %w", groups.ConfigName, invalid[0].Err)
	}
	return fmt.Errorf("no %s at or above %s\nhint: `oberth init` writes one, or name a project: `oberth up <project>`",
		groups.ConfigName, shortPath(dir))
}

// upDocument is the `up --json` answer: one entry per service plus the summary
// fields. Next is the confirmation call when this invocation started services
// without waiting for readiness; it is absent when the caller already waited.
type upDocument struct {
	Services []rpc.GroupsStartChunk `json:"services"`
	Next     string                 `json:"next,omitempty"`
	rpc.GroupsStartEnd
}

// consumeStart prints one line per service as the daemon reports it, then the
// summary. It exits non-zero when any service failed to start (spec, "Error
// handling": a partial failure is still a failure).
func consumeStart(stream *client.Stream, asJSON bool) error {
	return consumeStartWithWait(context.Background(), nil, stream, asJSON, false, 0, false, rpc.GroupsStartParams{}, nil)
}

func consumeStartWithWait(ctx context.Context, c *client.Client, stream *client.Stream, asJSON, wait bool,
	waitTimeout time.Duration, waitTimeoutSet bool, params rpc.GroupsStartParams, cfg *groups.Config) error {
	results, summary, err := collectStartChunks(stream, os.Stderr, wait, asJSON)
	if err != nil {
		return err
	}
	var cleanupErr error
	if wait {
		if !asJSON {
			fmt.Println(display.Dim("waiting for services to be ready…"))
		}
		timedOut, err := waitForStartReady(ctx, params, cfg, results, &summary, waitTimeout, waitTimeoutSet)
		if err != nil {
			return err
		}
		if len(timedOut) > 0 && c != nil {
			cleanupErr = stopTimedOutServices(ctx, c, params, timedOut)
		}
	}

	next := readinessNext(results, wait)
	if asJSON {
		if err := printJSON(upDocument{Services: results, Next: next, GroupsStartEnd: summary}); err != nil {
			return err
		}
	} else {
		if wait {
			for _, result := range results {
				printStartChunk(result)
			}
		}
		printStartSummary(summary)
		if next != "" {
			fmt.Println(display.Dim("next: " + next))
		}
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

// killCaller is the part of the daemon client a timeout cleanup needs: one
// call and the capability list. It is an interface so the stop request can be
// tested without a live daemon.
type killCaller interface {
	Call(ctx context.Context, method string, params, result any) error
	Hello() rpc.DaemonHelloResult
}

// stopTimedOutServices makes a readiness timeout terminal. The same group
// mutation path used by `oberth down` stops portless workers as well as
// listeners, but the stop names the run this invocation started for each
// service. A service name alone would also match a run another controller
// started while this wait was running — that run is not ours to stop and must
// survive the timeout. `expect_runs` also keeps the stop scoped, so
// already-running or skipped services and their `port: auto` claims stay.
//
// A daemon that does not know `expect_runs` would silently drop the field and
// stop by name, which is exactly the wrong run; the cleanup is refused then,
// and an invocation that cannot name the run it started refuses too.
func stopTimedOutServices(ctx context.Context, c killCaller, params rpc.GroupsStartParams, timedOut []startTimeout) error {
	if !hasCapability(c.Hello(), rpc.CapabilityKillExpectRuns) {
		return timeoutCleanupRefused(c.Hello(), timedOut,
			"does not announce "+rpc.CapabilityKillExpectRuns+", so it would stop whatever answers to the service name")
	}
	expect := make([]rpc.RunExpectation, 0, len(timedOut))
	for _, timedOutService := range timedOut {
		if timedOutService.runID == "" {
			return timeoutCleanupRefused(c.Hello(), timedOut,
				"did not name the run it started for "+timedOutService.service)
		}
		expect = append(expect, rpc.RunExpectation{
			Service: timedOutService.service,
			RunID:   timedOutService.runID,
		})
	}
	var env rpc.KillEnvelope
	err := c.Call(ctx, "groups.kill", rpc.GroupsKillParams{
		Name:       valueOrEmpty(params.Name),
		ConfigPath: params.ConfigPath,
		ExpectRuns: expect,
		Release:    true,
		Reason:     "ready_timeout",
	}, &env)
	if err != nil {
		return cliError(err)
	}
	return nil
}

// timeoutCleanupRefused says plainly that the timed-out runs were left running
// and why: a cleanup that stops the wrong run must not read as success, and an
// agent needs the state it is actually in.
func timeoutCleanupRefused(hello rpc.DaemonHelloResult, timedOut []startTimeout, why string) error {
	version := hello.DaemonVersion
	if version == "" {
		version = "an older version"
	}
	services := make([]string, 0, len(timedOut))
	for _, service := range timedOut {
		services = append(services, service.service)
	}
	return fmt.Errorf("the timed-out runs for %s were not stopped: the running daemon (%s) %s\nhint: they may still be running; stop them with `oberth down`, or restart the daemon with `oberth daemon restart`",
		strings.Join(services, ", "), version, why)
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// startChunkStream is the bit of client.Stream this command reads. It lets the
// chunk handling be tested without a live daemon.
type startChunkStream interface {
	Chunks() <-chan json.RawMessage
	End() <-chan client.End
}

// collectStartChunks drains a groups.start stream into the per-service
// outcomes and the summary. In-flight `starting` chunks are diagnostic: they
// go to progress (stderr in production) and are dropped here, so services[],
// the readiness wait and the summary never treat them as services.
func collectStartChunks(stream startChunkStream, progress io.Writer, wait, asJSON bool) ([]rpc.GroupsStartChunk, rpc.GroupsStartEnd, error) {
	var outcomes []rpc.GroupsStartChunk
	for payload := range stream.Chunks() {
		var outcome rpc.GroupsStartChunk
		if err := json.Unmarshal(payload, &outcome); err != nil {
			continue
		}
		if isStartProgressChunk(outcome) {
			// A daemon may still send progress when a peer asked for it;
			// this invocation's own contract decides whether it is shown.
			if !asJSON {
				printStartProgress(progress, outcome)
			}
			continue
		}
		normalizeStartChunk(&outcome)
		if !wait {
			// This invocation did not wait, so it cannot claim readiness.
			// Say only what it knows: the daemon reported a started process.
			markStartChunkUnconfirmed(&outcome)
		}
		outcomes = append(outcomes, outcome)
		if !asJSON && !wait {
			printStartChunk(outcome)
		}
	}
	end := <-stream.End()
	var summary rpc.GroupsStartEnd
	if end.Err != nil {
		return outcomes, summary, cliError(end.Err)
	}
	if err := end.Decode(&summary); err != nil {
		return outcomes, summary, err
	}
	return outcomes, summary, nil
}

// isStartProgressChunk reports whether a chunk is an in-flight observation
// requested with progress, not a service outcome. Such chunks are filtered
// before readiness waiting, JSON assembly and the summary.
func isStartProgressChunk(c rpc.GroupsStartChunk) bool {
	return c.State == "starting"
}

// printStartProgress writes one dim progress line. It goes to stderr so a
// `--json` invocation still emits exactly one JSON value on stdout.
func printStartProgress(w io.Writer, c rpc.GroupsStartChunk) {
	parts := make([]string, 0, 3)
	if c.Phase != "" {
		parts = append(parts, c.Phase)
	}
	if len(c.WaitingOn) > 0 {
		parts = append(parts, "waiting on "+strings.Join(c.WaitingOn, ", "))
	}
	if c.ElapsedMS > 0 {
		parts = append(parts, fmt.Sprintf("%dms", c.ElapsedMS))
	}
	if c.LastPrepareMS != nil {
		parts = append(parts, fmt.Sprintf("last prepare %dms", *c.LastPrepareMS))
	}
	line := strings.Join(parts, " · ")
	if c.ReadyTimeout != "" {
		line += " (ready_timeout " + c.ReadyTimeout + ")"
	}
	fmt.Fprintf(w, "  %s %s  %s\n", display.Dim("…"), display.Bold(c.Service), display.Dim(line))
}

// normalizeStartChunk keeps a new CLI useful with a daemon that predates the
// structured outcome fields. The daemon remains the source of facts; this is
// only a compatibility projection for the client-side JSON document.
func normalizeStartChunk(c *rpc.GroupsStartChunk) {
	// A daemon that sends the structured outcome fields also decides its own
	// next step: it knows whether a log exists, and it knows when a refusal
	// carries its own way out. Only a daemon predating those fields needs the
	// projection below — second-guessing the new one put a "read the logs" hint
	// on services that never started.
	structured := c.State != ""
	if !structured {
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
	if c.Error != "" && c.Hint == "" && !structured {
		c.Hint = logsHint(c.Service)
	}
}

// markStartChunkUnconfirmed states what this invocation knows: the daemon
// reported the process started, but this command did not wait for readiness.
// It is an annotation on the CLI's own output, not a daemon fact.
func markStartChunkUnconfirmed(c *rpc.GroupsStartChunk) {
	if c.State != "started" {
		return
	}
	ready := false
	c.Ready = &ready
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
		note := ""
		if len(c.IncludedBy) > 0 {
			note = "  " + display.Dim("(dependency of "+strings.Join(c.IncludedBy, ", ")+")")
		}
		fmt.Printf("  %s %s  %s%s\n", display.Red("x"), display.Bold(c.Service), display.Dim(c.Error), note)
		if c.Hint != "" {
			fmt.Printf("    %s\n", display.Dim("next: "+c.Hint))
		}
	case c.Skipped:
		reason := c.Reason
		if reason == "" {
			reason = "already running"
		}
		note := ""
		if len(c.IncludedBy) > 0 {
			note = "  " + display.Dim("· required by "+strings.Join(c.IncludedBy, ", "))
		}
		fmt.Printf("  %s %s  %s%s\n", display.Dim("-"), display.Bold(c.Service), display.Dim(reason), note)
		if c.Hint != "" {
			fmt.Printf("    %s\n", display.Dim("next: "+c.Hint))
		}
	default:
		// The address, not the port number: a terminal makes a URL clickable,
		// and opening the thing you just started is the next thing you do.
		location := display.Dim(fmt.Sprintf("pid %d  %s", c.PID, shortPath(c.LogPath)))
		if c.Port > 0 {
			location = display.Underline(groups.URL(c.Port)) + "  " + location
		}
		marker, suffix := display.Green("✓"), ""
		if c.Ready == nil || !*c.Ready {
			// A check mark would claim the process is ready; this invocation
			// only saw it start.
			marker, suffix = display.Yellow("~"), "  "+display.Dim("started, readiness not confirmed")
		}
		if len(c.IncludedBy) > 0 {
			suffix += "  " + display.Dim("(dependency of "+strings.Join(c.IncludedBy, ", ")+")")
		}
		fmt.Printf("  %s %s  %s%s\n", marker, display.Bold(c.Service), location, suffix)
	}
}

// readinessNext names the call that confirms readiness for the services this
// invocation only started. Empty when the caller waited or nothing started
// unconfirmed: `started` plus `ready:false` means "not confirmed", not failed,
// and the next step must be published rather than left to a socket poll.
func readinessNext(results []rpc.GroupsStartChunk, wait bool) string {
	if wait {
		return ""
	}
	var names []string
	for _, chunk := range results {
		if chunk.State != "started" {
			continue
		}
		if chunk.Ready != nil && *chunk.Ready {
			continue
		}
		names = append(names, chunk.Service)
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return "oberth up --only " + strings.Join(names, ",") + " --wait --json"
}

func printStartSummary(end rpc.GroupsStartEnd) {
	parts := []string{fmt.Sprintf("%d started", len(end.Started))}
	switch {
	case len(end.Skipped) > 0:
		// The per-service line carries why it was skipped; "already running"
		// here would claim ownership for a listener option-berth did not start.
		parts = append(parts, fmt.Sprintf("%d skipped", len(end.Skipped)))
	}
	if len(end.Errors) > 0 {
		parts = append(parts, display.Red(fmt.Sprintf("%d failed", len(end.Errors))))
	}
	fmt.Printf("\n%s\n", display.Dim(strings.Join(parts, ", ")))
	if len(end.Included) > 0 {
		fmt.Printf("%s\n", display.Dim("included dependencies: "+strings.Join(end.Included, ", ")))
	}
}

// shortPath renders a path under the home directory as ~/….
func shortPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	switch {
	case home == "", path == "":
		return path
	case !strings.HasPrefix(path, home):
		return path
	}
	rest, err := filepath.Rel(home, path)
	if err != nil {
		return path
	}
	return filepath.Join("~", rest)
}
