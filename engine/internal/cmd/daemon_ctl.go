package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/spf13/cobra"
)

var (
	daemonJSONFlag   bool
	daemonFollowFlag bool
	daemonLinesFlag  int
)

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether the daemon is running, and what it is doing",
	Args:  cobra.NoArgs,
	RunE:  daemonStatusRun,
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running daemon",
	Args:  cobra.NoArgs,
	RunE:  daemonStopRun,
}

var daemonRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Stop the daemon if it is running, then start it detached",
	Args:  cobra.NoArgs,
	RunE:  daemonRestartRun,
}

var daemonPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the socket path the daemon listens on",
	Args:  cobra.NoArgs,
	RunE:  daemonPathRun,
}

var daemonLogCmd = &cobra.Command{
	Use:   "log",
	Short: "Print the daemon log",
	Args:  cobra.NoArgs,
	RunE:  daemonLogRun,
}

func init() {
	daemonStatusCmd.Flags().BoolVar(&daemonJSONFlag, "json", false, "Output as JSON")
	daemonPathCmd.Flags().BoolVar(&daemonJSONFlag, "json", false, "Output as JSON")
	daemonStopCmd.Flags().BoolVar(&daemonJSONFlag, "json", false, "Output as JSON")
	daemonRestartCmd.Flags().BoolVar(&daemonJSONFlag, "json", false, "Output as JSON")
	daemonLogCmd.Flags().BoolVarP(&daemonFollowFlag, "follow", "f", false, "Follow the log as it grows")
	daemonLogCmd.Flags().IntVarP(&daemonLinesFlag, "lines", "n", 50, "Number of trailing lines to print")

	daemonCmd.AddCommand(daemonStatusCmd, daemonStopCmd, daemonRestartCmd, daemonPathCmd, daemonLogCmd)
}

// connectRunning dials an already-running daemon. `oberth daemon` never
// autostarts: asking a daemon about itself must not create one.
func connectRunning(ctx context.Context) (*client.Client, error) {
	return client.Dial(ctx, client.ClientInfo{
		Name:        "cli",
		Version:     buildinfo.Version,
		NoAutostart: true,
	})
}

// notRunning is the answer when no daemon is listening. In JSON mode that is
// still an answer rather than an error — `daemon status --json` documents
// `{"running": false}` on stdout — so it prints and exits 1 quietly; otherwise
// it becomes the standard error-plus-hint on stderr.
func notRunning(socket string) error {
	if daemonJSONFlag {
		if err := printJSON(map[string]any{"running": false, "socket": socket}); err != nil {
			return err
		}
		return errSilent
	}
	return failHint("daemon_not_running",
		"oberth daemon is not running",
		"start it with `oberth serve` (or `oberth serve -d` to detach)")
}

func daemonStatusRun(cmd *cobra.Command, _ []string) error {
	c, err := runningClient(cmd.Context())
	if err != nil {
		return err
	}
	defer c.Close()

	var status rpc.DaemonStatusResult
	if err := c.Call(cmd.Context(), "daemon.status", rpc.Empty{}, &status); err != nil {
		return err
	}
	doc := daemonStatus{status: status, hello: c.Hello(), cliCommit: buildinfo.Commit}

	if daemonJSONFlag {
		return printJSON(doc.jsonDoc())
	}
	doc.writeText(os.Stdout)
	if note := staleDaemonNote(status.Commit, buildinfo.Commit); note != "" {
		fmt.Fprintln(os.Stderr, display.Dim(note))
	}
	return nil
}

// runningClient dials a daemon that must already be running. Absence is the
// documented not-running answer, not an error to autostart around: `oberth
// daemon` never creates the daemon it is inspecting.
func runningClient(ctx context.Context) (*client.Client, error) {
	c, err := connectRunning(ctx)
	if err != nil {
		if errors.Is(err, client.ErrNotRunning) {
			return nil, notRunning(daemon.SocketPath())
		}
		return nil, err
	}
	return c, nil
}

// daemonStatus is one read of the daemon, built once so the JSON document and
// the text lines can never describe different values. cliCommit is this
// process's build; the comparison with the daemon's is part of the answer.
type daemonStatus struct {
	status    rpc.DaemonStatusResult
	hello     rpc.DaemonHelloResult
	cliCommit string
}

func (s daemonStatus) jsonDoc() map[string]any {
	body := map[string]any{
		"running":               true,
		"pid":                   s.status.PID,
		"uptime":                s.status.Uptime,
		"subscribers":           s.status.Subscribers,
		"last_scan_at":          s.status.LastScanAt,
		"scan_interval_ms":      s.status.ScanIntervalMs,
		"scan_base_interval_ms": s.status.ScanBaseIntervalMs,
		"stats_interval_ms":     s.status.StatsIntervalMs,
		"scans":                 s.status.Scans,
		"db_path":               s.status.DBPath,
		"socket":                s.hello.Socket,
		"daemon_version":        s.hello.DaemonVersion,
		"protocol_version":      s.hello.ProtocolVersion,
		"capabilities":          s.hello.Capabilities,
		"build_commit":          s.status.Commit,
		"build_date":            s.status.Built,
		"build_matches":         s.status.Commit == "" || s.status.Commit == s.cliCommit,
	}
	if s.status.Observation != nil {
		body["observation"] = s.status.Observation
	}
	return body
}

func (s daemonStatus) writeText(w io.Writer) {
	line := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }
	line("running       yes")
	line("pid           %d", s.status.PID)
	line("version       %s (protocol %s)", s.hello.DaemonVersion, s.hello.ProtocolVersion)
	line("build         %s, built %s", orUnknown(s.status.Commit), orUnknown(s.status.Built))
	line("uptime        %s", s.status.Uptime)
	line("subscribers   %d", s.status.Subscribers)
	line("scan interval %dms", s.status.ScanIntervalMs)
	line("scan base     %dms", s.status.ScanBaseIntervalMs)
	line("stats tick    %dms", s.status.StatsIntervalMs)
	line("scans         %d", s.status.Scans)
	if s.status.LastScanAt != "" {
		line("last scan     %s", s.status.LastScanAt)
	}
	line("socket        %s", s.hello.Socket)
	// An observable daemon needs no row: the socket line already says the
	// daemon is working. Anything else is the fact a caller must not miss.
	if obs := s.status.Observation; obs != nil && obs.State != string(ports.ObservationObservable) {
		line("observation   %s (%s)", obs.State, orUnknown(obs.Code))
		if obs.Hint != "" {
			line("hint          %s", obs.Hint)
		}
	}
	if s.status.DBPath != "" {
		line("database      %s", s.status.DBPath)
	}
	line("capabilities  %v", s.hello.Capabilities)
}

// staleDaemonNote is empty when the daemon is this build, and otherwise says
// what to do about it.
//
// A daemon is started once and keeps running: after any change to the engine it
// is still answering from the previous build, and nothing else in `daemon
// status` shows that — the version string is the same either way. It is the one
// failure this software cannot detect from the inside, so it is checked here,
// where the two builds meet.
func staleDaemonNote(daemonCommit, cliCommit string) string {
	if daemonCommit == "" || cliCommit == "" || daemonCommit == cliCommit {
		return ""
	}
	return fmt.Sprintf("note: the running daemon is from another build (%s; this CLI is %s) — "+
		"restart it to get this one: oberth daemon restart", daemonCommit, cliCommit)
}

// orUnknown keeps a missing field from printing as an empty gap.
func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func daemonStopRun(cmd *cobra.Command, _ []string) error {
	socket := daemon.SocketPath()
	c, err := runningClient(cmd.Context())
	if err != nil {
		return err
	}
	defer c.Close()

	var ok rpc.OKResult
	if err := c.Call(cmd.Context(), "daemon.shutdown", rpc.Empty{}, &ok); err != nil {
		return err
	}
	if err := waitForDaemonGone(socket, stopTimeout); err != nil {
		return err
	}
	if daemonJSONFlag {
		return printJSON(map[string]any{"stopped": true, "socket": socket})
	}
	fmt.Println("oberth daemon stopped")
	return nil
}

func daemonRestartRun(cmd *cobra.Command, _ []string) error {
	socket := daemon.SocketPath()
	if c, err := connectRunning(cmd.Context()); err == nil {
		var ok rpc.OKResult
		_ = c.Call(cmd.Context(), "daemon.shutdown", rpc.Empty{}, &ok)
		c.Close()
		// The old daemon releases its lock after it closes its socket, so the
		// replacement must wait for the lock, not for the socket.
		if err := waitForDaemonGone(socket, stopTimeout); err != nil {
			return err
		}
	}
	if err := detachDaemon(cmd.Context(), socket); err != nil {
		return err
	}
	if daemonJSONFlag {
		return printJSON(map[string]any{"restarted": true, "socket": socket})
	}
	return nil
}

func daemonPathRun(*cobra.Command, []string) error {
	socket := daemon.SocketPath()
	if daemonJSONFlag {
		return printJSON(map[string]any{
			"socket": socket,
			"lock":   daemon.LockPath(),
			"log":    daemon.LogPath(),
		})
	}
	fmt.Println(socket)
	return nil
}

func daemonLogRun(cmd *cobra.Command, _ []string) error {
	path := daemon.LogPath()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no daemon log at %s yet — start the daemon with `oberth serve`", path)
		}
		return err
	}
	defer f.Close()

	if err := printTail(f, daemonLinesFlag); err != nil {
		return err
	}
	if !daemonFollowFlag {
		return nil
	}
	return followFile(cmd.Context(), f)
}

// printTail writes the last n lines of f to stdout and leaves the file offset
// at the end, ready for follow mode.
func printTail(f *os.File, n int) error { return printTailTo(f, n, os.Stdout) }
func printTailTo(f *os.File, n int, out io.Writer) error {
	if n <= 0 {
		_, err := f.Seek(0, io.SeekEnd)
		return err
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	// Allocate for actual observed lines rather than a potentially enormous flag.
	ring := make([]string, 0, min(n, 128))
	next := 0
	for scanner.Scan() {
		if len(ring) < n {
			ring = append(ring, scanner.Text())
		} else {
			ring[next] = scanner.Text()
			next = (next + 1) % n
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	for i := range ring {
		if err := writeLogBytes(out, []byte(ring[(next+i)%len(ring)]+"\n")); err != nil {
			return err
		}
	}
	return nil
}

// A closed consumer is a delivery failure, not a reason to keep following.
func followFile(ctx context.Context, f *os.File) error { return followFileTo(ctx, f, os.Stdout) }
func followFileTo(ctx context.Context, f *os.File, out io.Writer) error {
	buf := make([]byte, 32*1024)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := f.Read(buf)
		if n > 0 {
			if err := writeLogBytes(out, buf[:n]); err != nil {
				return err
			}
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if n > 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func writeLogBytes(out io.Writer, data []byte) error {
	n, err := out.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}
