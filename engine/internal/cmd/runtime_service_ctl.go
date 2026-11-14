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
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/spf13/cobra"
)

var (
	// JSON prints the contract document instead of the human text.
	runtimeServiceJSONFlag bool
	// Follow keeps the log open after the tail has been printed.
	runtimeServiceFollowFlag bool
	// Lines is how much of the log the tail prints first.
	runtimeServiceLinesFlag int
)

var runtimeServiceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether the runtime service is running, and what it is doing",
	RunE:  runtimeServiceStatusRun,
	Args:  cobra.NoArgs,
}

var runtimeServiceStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running runtime service",
	RunE:  runtimeServiceStopRun,
	Args:  cobra.NoArgs,
}

var runtimeServiceRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Stop the runtime service if it is running, then start it detached",
	RunE:  runtimeServiceRestartRun,
	Args:  cobra.NoArgs,
}

var runtimeServicePathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the socket path the runtime service listens on",
	RunE:  runtimeServicePathRun,
	Args:  cobra.NoArgs,
}

var runtimeServiceLogCmd = &cobra.Command{
	Use:   "log",
	Short: "Print the runtime service log",
	RunE:  runtimeServiceLogRun,
	Args:  cobra.NoArgs,
}

func init() {
	for _, command := range []*cobra.Command{runtimeServiceStatusCmd, runtimeServicePathCmd, runtimeServiceStopCmd, runtimeServiceRestartCmd} {
		command.Flags().BoolVar(&runtimeServiceJSONFlag, "json", false, "Output as JSON")
	}
	runtimeServiceLogCmd.Flags().BoolVarP(&runtimeServiceFollowFlag, "follow", "f", false, "Follow the log as it grows")
	runtimeServiceLogCmd.Flags().IntVarP(&runtimeServiceLinesFlag, "lines", "n", 50, "Number of trailing lines to print")
	runtimeServiceCmd.AddCommand(runtimeServiceStatusCmd, runtimeServiceStopCmd, runtimeServiceRestartCmd, runtimeServicePathCmd, runtimeServiceLogCmd)
}

// connectRunning dials an already-running runtime.service. `oberth runtime service` never
// autostarts: asking a runtime service about itself must not create one.
func connectRunning(ctx context.Context) (*client.Client, error) {
	return client.Dial(ctx, client.ClientInfo{
		Name:        "cli",
		Version:     buildinfo.Version,
		NoAutostart: true,
	})
}

// notRunning is the answer when no runtime service is listening. In JSON mode that is
// still an answer rather than an error — `runtime service status --json` documents
// `{"running": false}` on stdout — so it prints and exits 1 quietly; otherwise
// it becomes the standard error-plus-hint on stderr.
func notRunning(socket string) error {
	if runtimeServiceJSONFlag {
		if err := printJSON(map[string]any{"running": false, "socket": socket}); err != nil {
			return err
		}
		return errSilent
	}
	return failHint("runtime_service_not_running",
		"oberth runtime service is not running",
		"start it with `oberth runtime service run` (or `oberth runtime service run -d` to detach)")
}

func runtimeServiceStatusRun(cmd *cobra.Command, _ []string) error {
	conn, err := runningClient(cmd.Context())
	if err != nil {
		return err
	}
	defer conn.Close()

	var status rpc.RuntimeServiceStatusResult
	if callErr := conn.Call(cmd.Context(), "runtime.service.status", rpc.Empty{}, &status); callErr != nil {
		return callErr
	}
	doc := runtimeServiceStatus{status: status, hello: conn.Hello(), cliCommit: buildinfo.Commit}

	if runtimeServiceJSONFlag {
		return printJSON(doc.jsonDoc())
	}
	doc.writeText(os.Stdout)
	if note := staleRuntimeServiceNote(status.Commit, buildinfo.Commit); note != "" {
		fmt.Fprintln(os.Stderr, display.Dim(note))
	}
	return nil
}

// runningClient dials a runtime service that must already be running. Absence is the
// documented not-running answer, not an error to autostart around: `oberth
// runtime service` never creates the runtime service it is inspecting.
func runningClient(ctx context.Context) (*client.Client, error) {
	c, err := connectRunning(ctx)
	if err != nil {
		if errors.Is(err, client.ErrNotRunning) {
			return nil, notRunning(runtimesvc.SocketPath())
		}
		return nil, err
	}
	return c, nil
}

// runtimeServiceStatus is one read of the runtime service, built once so the JSON document and
// the text lines can never describe different values. cliCommit is this
// process's build; the comparison with the runtime service's is part of the answer.
type runtimeServiceStatus struct {
	status    rpc.RuntimeServiceStatusResult
	hello     rpc.RuntimeServiceHelloResult
	cliCommit string
}

func (s runtimeServiceStatus) jsonDoc() map[string]any {
	body := map[string]any{
		"running":                 true,
		"pid":                     s.status.PID,
		"uptime":                  s.status.Uptime,
		"subscribers":             s.status.Subscribers,
		"last_scan_at":            s.status.LastScanAt,
		"scan_interval_ms":        s.status.ScanIntervalMs,
		"scan_base_interval_ms":   s.status.ScanBaseIntervalMs,
		"stats_interval_ms":       s.status.StatsIntervalMs,
		"scans":                   s.status.Scans,
		"db_path":                 s.status.DBPath,
		"socket":                  s.hello.Socket,
		"runtime_service_version": s.hello.RuntimeServiceVersion,
		"protocol_version":        s.hello.ProtocolVersion,
		"capabilities":            s.hello.Capabilities,
		"build_commit":            s.status.Commit,
		"build_date":              s.status.Built,
		"build_matches":           s.status.Commit == "" || s.status.Commit == s.cliCommit,
	}
	if s.status.Observation != nil {
		body["observation"] = s.status.Observation
	}
	return body
}

func (s runtimeServiceStatus) writeText(w io.Writer) {
	line := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }
	line("running       yes")
	line("pid           %d", s.status.PID)
	line("version       %s (protocol %s)", s.hello.RuntimeServiceVersion, s.hello.ProtocolVersion)
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
	// An observable runtime service needs no row: the socket line already says the
	// runtime service is working. Anything else is the fact a caller must not miss.
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

// staleRuntimeServiceNote is empty when the runtime service is this build, and otherwise says
// what to do about it.
//
// A runtime service is started once and keeps running: after any change to the engine it
// is still answering from the previous build, and nothing else in `runtime service
// status` shows that — the version string is the same either way. It is the one
// failure this software cannot detect from the inside, so it is checked here,
// where the two builds meet.
func staleRuntimeServiceNote(runtimeServiceCommit, cliCommit string) string {
	if runtimeServiceCommit == "" || cliCommit == "" || runtimeServiceCommit == cliCommit {
		return ""
	}
	return fmt.Sprintf("note: the running runtime service is from another build (%s; this CLI is %s) — "+
		"restart it to get this one: oberth runtime service restart", runtimeServiceCommit, cliCommit)
}

// orUnknown keeps a missing field from printing as an empty gap.
func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func runtimeServiceStopRun(cmd *cobra.Command, _ []string) error {
	socket := runtimesvc.SocketPath()
	conn, err := runningClient(cmd.Context())
	if err != nil {
		return err
	}
	defer conn.Close()

	var ack rpc.OKResult
	if callErr := conn.Call(cmd.Context(), "runtime.service.shutdown", rpc.Empty{}, &ack); callErr != nil {
		return callErr
	}
	if waitErr := waitForRuntimeServiceGone(socket, stopTimeout); waitErr != nil {
		return waitErr
	}
	if runtimeServiceJSONFlag {
		return printJSON(map[string]any{"stopped": true, "socket": socket})
	}
	fmt.Println("oberth runtime service stopped")
	return nil
}

func runtimeServiceRestartRun(cmd *cobra.Command, _ []string) error {
	socket := runtimesvc.SocketPath()
	if conn, dialErr := connectRunning(cmd.Context()); dialErr == nil {
		var ack rpc.OKResult
		_ = conn.Call(cmd.Context(), "runtime.service.shutdown", rpc.Empty{}, &ack)
		conn.Close()
		// The old runtime service releases its lock only after closing its socket, so the
		// replacement waits for the lock rather than for the socket.
		if waitErr := waitForRuntimeServiceGone(socket, stopTimeout); waitErr != nil {
			return waitErr
		}
	}
	if detachErr := detachRuntimeService(cmd.Context(), socket); detachErr != nil {
		return detachErr
	}
	if runtimeServiceJSONFlag {
		return printJSON(map[string]any{"restarted": true, "socket": socket})
	}
	return nil
}

func runtimeServicePathRun(*cobra.Command, []string) error {
	socket := runtimesvc.SocketPath()
	if runtimeServiceJSONFlag {
		return printJSON(map[string]any{
			"socket": socket,
			"lock":   runtimesvc.LockPath(),
			"log":    runtimesvc.LogPath(),
		})
	}
	fmt.Println(socket)
	return nil
}

func runtimeServiceLogRun(cmd *cobra.Command, _ []string) error {
	logPath := runtimesvc.LogPath()
	logFile, openErr := os.Open(logPath)
	if openErr != nil {
		if os.IsNotExist(openErr) {
			return fmt.Errorf("no runtime service log at %s yet — start the runtime service with `oberth runtime service run`", logPath)
		}
		return openErr
	}
	defer logFile.Close()

	if tailErr := printTail(logFile, runtimeServiceLinesFlag); tailErr != nil {
		return tailErr
	}
	if !runtimeServiceFollowFlag {
		return nil
	}
	return followFile(cmd.Context(), logFile)
}

// printTail writes the last n lines of f to stdout and leaves the file offset
// at the end, ready for follow mode.
func printTail(f *os.File, n int) error { return printTailTo(f, n, os.Stdout) }
func printTailTo(f *os.File, n int, out io.Writer) error {
	if n <= 0 {
		_, seekErr := f.Seek(0, io.SeekEnd)
		return seekErr
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
