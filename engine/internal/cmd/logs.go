package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/sheathedsharp/option-berth/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/display"
	"github.com/sheathedsharp/option-berth/internal/docker"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/runs"
	"github.com/spf13/cobra"
)

var (
	logsFollow    bool
	logsLinesFlag int
	logsJSONFlag  bool
	logsOnceFlag  bool
)

var logsCmd = &cobra.Command{
	Use:               "logs <service|port>",
	Short:             "Attach to a process and view its log output",
	GroupID:           commandGroupCore,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeLogTarget,
	RunE: func(cmd *cobra.Command, args []string) error {
		bindIP, _ := cmd.Flags().GetString("ip")
		if logsOnceFlag {
			logsFollow = false
		}

		c := daemonClient(cmd.Context())
		defer func() {
			if c != nil {
				c.Close()
			}
		}()
		// Keep the direct port path's contract: structured output needs the
		// daemon, while human output may fall back to the local process tools.
		if c == nil {
			if port, err := strconv.Atoi(args[0]); err == nil {
				return logsDirect(cmd.Context(), port, bindIP)
			}
		}
		row, logPath, err := resolveLogTarget(cmd.Context(), c, args[0], bindIP)
		if err != nil {
			return err
		}
		meta := logMetadataForTarget(cmd.Context(), c, args[0], row)

		if logPath != "" {
			return logsFromFile(cmd.Context(), logPath, meta)
		}

		if c != nil {
			return logsThroughDaemon(cmd.Context(), c, row.Port, bindIP, meta)
		}

		return logsDirect(cmd.Context(), row.Port, bindIP)
	},
}

func logsDirect(_ context.Context, port int, bindIP string) error {
	// The direct path execs into tail/docker/log, handing the terminal to
	// another process: it has no output of its own left to structure, so --json
	// is served by the daemon only.
	if logsJSONFlag {
		return failHint("daemon_unavailable",
			"logs --json needs the daemon",
			"start one with `oberth serve --detach`")
	}
	lp, err := ports.FindByPort(port, bindIP)
	if err != nil {
		return err
	}

	// Enrich to get Docker info and full command. Run ownership is stamped
	// here too: this path renders the listener without building the group
	// collection, and the run's name is what the header should show.
	enriched := []ports.ListeningPort{*lp}
	docker.EnrichPorts(enriched)
	ports.Enrich(enriched)
	groups.StampRuns(enriched)
	*lp = enriched[0]

	fmt.Printf("%s %s (PID %s)\n\n",
		display.Dim("Attaching to"),
		display.Bold(lp.DisplayName()),
		display.Cyan(fmt.Sprintf("%d", lp.PID)))

	// Docker containers: use docker logs.
	if lp.Type == ports.PortTypeDocker && lp.DockerContainer != "" {
		return execDockerLogs(lp.DockerContainer)
	}

	// Windows: log discovery is not supported.
	if runtime.GOOS == "windows" {
		return fmt.Errorf("log viewing is not supported on Windows for non-Docker processes")
	}

	// Regular processes: find log sources via lsof.
	sources := ports.FindLogSources(lp.PID)
	if len(sources) > 0 {
		return tailLogSources(sources)
	}

	// Nothing else: a process that writes to a terminal or a pipe has no log
	// for anyone to read, and option-berth only collects the output of the
	// services it started.
	return tailProcFD(lp.PID)
}

// logTarget is either a listening process selected by port, or a service log
// file. Services without a port (workers) still have a log path because the
// daemon records every declared run, not only listeners.
func resolveLogTarget(ctx context.Context, c *client.Client, target, bindIP string) (*ports.ListeningPort, string, error) {
	if port, err := strconv.Atoi(target); err == nil {
		if c != nil {
			row, err := daemonFindPort(ctx, c, port, bindIP)
			return row, "", err
		}
		row, err := ports.FindByPort(port, bindIP)
		if err != nil {
			return nil, "", err
		}
		return row, "", nil
	}

	wd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}
	name, _ := projectAt(wd)
	if name == "" {
		return nil, "", failHint("not_found", fmt.Sprintf("no project contains service %q", target),
			"run this command inside a worktree with an oberth.yaml")
	}
	_, groups, err := groupRows(ctx)
	if err != nil {
		return nil, "", cliError(err)
	}
	for _, group := range groups {
		if group.Name != name {
			continue
		}
		for _, service := range group.Services {
			if service.Name != target {
				continue
			}
			// An explicitly named managed service already has an authoritative
			// log path. Do not rediscover it through lsof or duplicate stdout and
			// stderr through /proc merely because the service also listens.
			if path := preferredServiceLog(service); path != "" {
				return nil, path, nil
			}
			if service.PortActual != nil {
				if c != nil {
					row, err := daemonFindPort(ctx, c, *service.PortActual, bindIP)
					return row, "", err
				}
				row, err := ports.FindByPort(*service.PortActual, bindIP)
				if err != nil {
					return nil, "", err
				}
				return row, "", nil
			}
			return nil, "", failHint("not_found", fmt.Sprintf("service %q is not running", target),
				"run `oberth up`, or use `oberth status` to inspect its last exit")
		}
		return nil, "", failHint("not_found", fmt.Sprintf("no service named %q in %s", target, name),
			"run `oberth status` to see the declared service names")
	}
	return nil, "", failHint("not_found", fmt.Sprintf("no project named %q", name),
		"run `oberth status` to inspect the current worktree")
}

func logsFromFile(ctx context.Context, path string, meta logMetadata) error {
	if logsFollow {
		return streamFileLogs(ctx, path, logsJSONFlag, logsLinesFlag, meta)
	}
	doc, err := readFileLogs(path, logsLinesFlag)
	if err != nil {
		return err
	}
	applyLogMetadata(&doc, meta)
	if logsJSONFlag {
		return printJSON(doc)
	}
	printLogHeader(doc.Source)
	for _, line := range doc.Lines {
		fmt.Println(line)
	}
	return nil
}

func readFileLogs(path string, lines int) (logsDocument, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return logsDocument{}, failHint("not_found", "log file does not exist: "+path,
			"start the service with `oberth up`")
	}
	if err != nil {
		return logsDocument{}, err
	}
	all := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(all) == 1 && all[0] == "" {
		all = []string{}
	}
	if lines <= 0 {
		lines = 10
	}
	truncated := len(all) > lines
	if truncated {
		all = all[len(all)-lines:]
	}
	return logsDocument{Source: path, Lines: all, Truncated: truncated}, nil
}

func streamFileLogs(ctx context.Context, path string, asJSON bool, lines int, meta logMetadata) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return failHint("not_found", "log file does not exist: "+path,
			"start the service with `oberth up`")
	}
	if lines <= 0 {
		lines = 10
	}
	cmd := exec.CommandContext(ctx, "tail", "-n", strconv.Itoa(lines), "-f", path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer cmd.Wait()
	if !asJSON {
		printLogHeader(path)
	}
	var enc *json.Encoder
	if asJSON {
		enc = json.NewEncoder(os.Stdout)
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 32<<10), 1024*1024)
	for sc.Scan() {
		if asJSON {
			if err := enc.Encode(logLineJSON(path, sc.Text(), meta)); err != nil {
				return err
			}
		} else {
			fmt.Println(sc.Text())
		}
	}
	return sc.Err()
}

// logsThroughDaemon tails a port's output over the socket. The daemon owns the
// `tail` (or `docker logs`) process, so several clients watching the same port
// cost one reader, and the lines reach every one of them.
func logsThroughDaemon(ctx context.Context, c *client.Client, port int, bindIP string, meta logMetadata) error {
	row, err := daemonFindPort(ctx, c, port, bindIP)
	if err != nil {
		return err
	}

	if !logsFollow {
		doc, err := readLogsOnce(ctx, c, *row, logsLinesFlag, meta)
		if err != nil {
			return err
		}
		if logsJSONFlag {
			return printJSON(doc)
		}
		printLogHeader(doc.Source)
		for _, line := range doc.Lines {
			fmt.Println(line)
		}
		return nil
	}

	if !logsJSONFlag {
		// In JSON mode this header would be plain text in front of a JSON
		// document.
		fmt.Printf("%s %s (PID %s)\n\n",
			display.Dim("Attaching to"),
			display.Bold(row.DisplayName()),
			display.Cyan(fmt.Sprintf("%d", row.PID)))
	}

	params := rpc.PortsLogsParams{
		Selector: rpc.Selector{
			HostParams:  hostParams(),
			Port:        &row.Port,
			BindAddress: strPtrOrNil(row.BindAddress),
		},
		Lines:  logsLinesFlag,
		Follow: true,
	}

	var res rpc.PortsLogsResult
	s, err := c.Stream(ctx, "ports.logs", params, &res)
	if err != nil {
		return cliError(err)
	}
	defer s.Close()

	if logsJSONFlag {
		return streamLogsJSON(s, meta)
	}

	printLogHeader(res.Source)
	for raw := range s.Chunks() {
		var chunk rpc.PortsLogsChunk
		if err := json.Unmarshal(raw, &chunk); err != nil {
			continue
		}
		fmt.Println(chunk.Line)
	}
	if end := <-s.End(); end.Err != nil {
		return cliError(end.Err)
	}
	return nil
}

// readLogsOnce reads a port's output and stops: no following, no terminal.
//
// It is the half behind `--once`: no terminal to hand to `tail`, so it reads
// through the daemon once.
func readLogsOnce(ctx context.Context, c *client.Client, row ports.ListeningPort, lines int, meta logMetadata) (logsDocument, error) {
	params := rpc.PortsLogsParams{
		Selector: rpc.Selector{
			HostParams:  hostParams(),
			Port:        &row.Port,
			BindAddress: strPtrOrNil(row.BindAddress),
		},
		Lines:  lines,
		Follow: false,
	}
	var res rpc.PortsLogsResult
	if err := c.Call(ctx, "ports.logs", params, &res); err != nil {
		return logsDocument{}, cliError(err)
	}
	doc := logsDocument{
		Source:    res.Source,
		Lines:     nonNilStrings(res.Lines),
		Truncated: res.Truncated,
	}
	applyLogMetadata(&doc, meta)
	return doc, nil
}

// logsDocument is what `logs <port> --once --json` prints: where the lines came
// from, and the lines themselves.
type logsDocument struct {
	Source string `json:"source"`
	// Lines is always an array, never null.
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
	Service   string   `json:"service,omitempty"`
	RunID     string   `json:"run_id,omitempty"`
	PID       int      `json:"pid,omitempty"`
	Status    string   `json:"status,omitempty"`
	ExitCode  *int     `json:"exit_code,omitempty"`
	Reason    string   `json:"reason,omitempty"`
}

type logMetadata struct {
	Service  string
	RunID    string
	PID      int
	Status   string
	ExitCode *int
	Reason   string
}

func applyLogMetadata(doc *logsDocument, meta logMetadata) {
	doc.Service = meta.Service
	doc.RunID = meta.RunID
	doc.PID = meta.PID
	doc.Status = meta.Status
	doc.ExitCode = meta.ExitCode
	doc.Reason = meta.Reason
}

func logLineJSON(source, line string, meta logMetadata) map[string]any {
	out := map[string]any{"source": source, "line": line}
	if meta.Service != "" {
		out["service"] = meta.Service
	}
	if meta.RunID != "" {
		out["run_id"] = meta.RunID
	}
	if meta.PID > 0 {
		out["pid"] = meta.PID
	}
	if meta.Status != "" {
		out["status"] = meta.Status
	}
	if meta.ExitCode != nil {
		out["exit_code"] = *meta.ExitCode
	}
	if meta.Reason != "" {
		out["reason"] = meta.Reason
	}
	return out
}

// logMetadataForTarget is best effort: the log itself remains useful when a
// daemon is between scans or a legacy runs.json entry lacks the newer fields.
// The extra context lets an agent join output back to the service and run it
// came from without parsing a human header or guessing from a file path.
func logMetadataForTarget(ctx context.Context, c *client.Client, target string, row *ports.ListeningPort) logMetadata {
	meta := logMetadata{}
	if row != nil {
		meta.PID = row.PID
		meta.Status = "running"
		if row.RunID != "" || row.Tag != "" || row.RunGroup != "" {
			meta.Service = row.Tag
			meta.RunID = row.RunID
			if row.RunRootPID > 0 {
				meta.PID = row.RunRootPID
			}
		}
	}
	if _, err := strconv.Atoi(target); err == nil {
		return meta
	}
	meta.Service = target

	group := ""
	if wd, err := os.Getwd(); err == nil {
		group, _ = projectAt(wd)
	}
	if c != nil {
		var listed rpc.RunsListResult
		if err := c.Call(ctx, "runs.list", rpc.HostParams{Host: hostParams().Host}, &listed); err == nil {
			for _, rec := range listed.Runs {
				if rec.Name == target && (group == "" || rec.Group == group) {
					return logMetadataFromRun(rec)
				}
			}
			for _, rec := range listed.Exited {
				if rec.Name == target && (group == "" || rec.Group == group) {
					return logMetadataFromRun(rec)
				}
			}
		}
		return meta
	}

	for _, rec := range runs.Load().Active() {
		if rec.NameOf() == target && (group == "" || rec.GroupOf() == group) {
			meta.RunID = rec.ID
			meta.PID = rec.PID
			meta.Status = "running"
			return meta
		}
	}
	return meta
}

func logMetadataFromRun(rec rpc.RunRecord) logMetadata {
	return logMetadata{
		Service:  rec.Name,
		RunID:    rec.ID,
		PID:      rec.PID,
		Status:   rec.Status,
		ExitCode: rec.ExitCode,
		Reason:   rec.Reason,
	}
}

// streamLogsJSON prints one JSON object per line as the log arrives — the same
// NDJSON shape the subscription streams use (docs/cli.md). A follower has no
// end to wrap an array around, and buffering until one arrived would defeat
// the point of following.
func streamLogsJSON(s *client.Stream, meta logMetadata) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	for raw := range s.Chunks() {
		var chunk rpc.PortsLogsChunk
		if err := json.Unmarshal(raw, &chunk); err != nil {
			continue
		}
		if err := enc.Encode(logLineJSON(chunk.Source, chunk.Line, meta)); err != nil {
			return err
		}
	}
	if end := <-s.End(); end.Err != nil {
		return cliError(end.Err)
	}
	return nil
}

// printLogHeader reproduces what the direct path prints above the output: the
// files being tailed. A container's logs have never carried a header.
func printLogHeader(source string) {
	switch {
	case source == "" || strings.HasPrefix(source, "docker:"):
		return
	default:
		for _, part := range strings.Split(source, ", ") {
			fmt.Println(display.Dim("  " + part))
		}
		fmt.Println()
	}
}

// daemonFindPort resolves one port through state.snapshot, so the header a command
// prints names the same process the daemon is about to act on.
func daemonFindPort(ctx context.Context, c *client.Client, port int, bindIP string) (*ports.ListeningPort, error) {
	rows, err := hostSnapshot(ctx, c)
	if err != nil {
		return nil, cliError(err)
	}
	var matches []ports.ListeningPort
	for _, row := range rows {
		if row.Port != port {
			continue
		}
		if bindIP != "" && row.BindAddress != bindIP {
			continue
		}
		matches = append(matches, row)
	}
	switch {
	case len(matches) == 0 && bindIP != "":
		return nil, fail("not_found", "no process found listening on %s:%d", bindIP, port)
	case len(matches) == 0:
		return nil, fail("not_found", "no process found listening on port %d", port)
	case len(matches) == 1:
		return &matches[0], nil
	}
	addrs := make([]string, 0, len(matches))
	for _, m := range matches {
		addrs = append(addrs, m.BindAddress)
	}
	return nil, failHint("ambiguous",
		fmt.Sprintf("port %d is bound to multiple addresses: %s", port, strings.Join(addrs, ", ")),
		fmt.Sprintf("use --ip to specify which one (e.g. --ip %s)", addrs[0]))
}

func init() {
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", true, "Follow log output (stream continuously)")
	logsCmd.Flags().BoolVar(&logsOnceFlag, "once", false, "Print what is there and exit (same as --follow=false)")
	logsCmd.Flags().IntVarP(&logsLinesFlag, "lines", "n", 10, "Number of trailing lines to show before following")
	logsCmd.Flags().BoolVar(&logsJSONFlag, "json", false,
		"Output as JSON: one document when reading once, one object per line while following")
	logsCmd.Flags().String("ip", "", "Specify bind address when a port is bound to multiple IPs")
	rootCmd.AddCommand(logsCmd)
}

func completeLogTarget(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completePort(cmd, args, toComplete)
}
