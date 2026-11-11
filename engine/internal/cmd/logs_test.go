package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runs"
	"github.com/spf13/cobra"
)

func TestReadFileLogsReturnsTailAndSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	doc, err := readFileLogs(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Source != path || !doc.Truncated {
		t.Fatalf("doc = %+v, want source %q and truncated output", doc, path)
	}
	want := []string{"two", "three"}
	if len(doc.Lines) != len(want) || doc.Lines[0] != want[0] || doc.Lines[1] != want[1] {
		t.Fatalf("lines = %#v, want %#v", doc.Lines, want)
	}
}

// TestSelectBoundPortChoosesOneRow pins the lookup's three answers: the
// matching row, the documented not_found, and ambiguous with the --ip remedy.
func TestSelectBoundPortChoosesOneRow(t *testing.T) {
	rows := []ports.ListeningPort{
		{Port: 3000, BindAddress: "127.0.0.1"},
		{Port: 3000, BindAddress: "0.0.0.0"},
		{Port: 5432, BindAddress: "0.0.0.0"},
	}

	row, err := selectBoundPort(rows, 3000, "127.0.0.1")
	if err != nil || row == nil || row.BindAddress != "127.0.0.1" {
		t.Fatalf("row = %+v err = %v, want the bound listener", row, err)
	}

	_, err = selectBoundPort(rows, 3000, "")
	if code, _, _ := describe(err); code != "ambiguous" {
		t.Fatalf("code = %q, want ambiguous for a multi-bind port", code)
	}

	_, err = selectBoundPort(rows, 9999, "")
	if code, msg, _ := describe(err); code != "not_found" || !strings.Contains(msg, "9999") {
		t.Fatalf("code = %q msg = %q, want not_found naming the port", code, msg)
	}

	_, err = selectBoundPort(rows, 3000, "::1")
	if code, msg, _ := describe(err); code != "not_found" || !strings.Contains(msg, "::1:3000") {
		t.Fatalf("code = %q msg = %q, want not_found naming the bind address", code, msg)
	}
}

// TestLogSelectorCarriesPortAndOnlyANonEmptyBind: the wire selector names the
// port always and the bind address only when there is one.
func TestLogSelectorCarriesPortAndOnlyANonEmptyBind(t *testing.T) {
	sel := logSelector(ports.ListeningPort{Port: 3000, BindAddress: "127.0.0.1"})
	if sel.Port == nil || *sel.Port != 3000 || sel.BindAddress == nil || *sel.BindAddress != "127.0.0.1" {
		t.Fatalf("selector = %+v, want port and bind", sel)
	}
	sel = logSelector(ports.ListeningPort{Port: 5432})
	if sel.Port == nil || *sel.Port != 5432 || sel.BindAddress != nil {
		t.Fatalf("selector = %+v, want no bind field", sel)
	}
}

func TestReadFileLogsMissingFileIsNotFound(t *testing.T) {
	_, err := readFileLogs(filepath.Join(t.TempDir(), "missing.log"), 10)
	if err == nil {
		t.Fatal("readFileLogs returned nil for a missing file")
	}
	if got := exitCodeFor(err); got != exitFail {
		t.Fatalf("exit code = %d, want %d", got, exitFail)
	}
}

func TestLogMetadataReadsPortlessActiveRun(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	if err := runs.Add(runs.Entry{PID: os.Getpid(), ID: "run-1", Group: "", Name: "worker"}); err != nil {
		t.Fatal(err)
	}

	meta := logMetadataForTarget(context.Background(), nil, "worker", nil)
	if meta.Service != "worker" || meta.RunID != "run-1" || meta.PID != os.Getpid() || meta.Status != "running" {
		t.Fatalf("metadata = %+v, want the active worker run", meta)
	}
}

func TestLogMetadataUsesPortRunAttribution(t *testing.T) {
	row := &ports.ListeningPort{PID: 44, RunID: "run-2", Tag: "api", RunRootPID: 42}
	meta := logMetadataForTarget(context.Background(), nil, "3000", row)
	if meta.Service != "api" || meta.RunID != "run-2" || meta.PID != 42 || meta.Status != "running" {
		t.Fatalf("metadata = %+v, want port run attribution", meta)
	}
}

func TestResolveLogTargetFindsPortlessServiceLog(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "oberth.yaml"), []byte("name: demo\nservices:\n  - name: worker\n    cmd: echo worker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	prev := noDaemonFlag
	noDaemonFlag = true
	t.Cleanup(func() { noDaemonFlag = prev })

	row, path, err := resolveLogTarget(context.Background(), nil, "worker", "")
	if err != nil {
		t.Fatal(err)
	}
	if row != nil {
		t.Fatalf("row = %+v, want a portless service", row)
	}
	if !strings.HasSuffix(path, filepath.Join("logs", "demo", "worker.log")) {
		t.Fatalf("log path = %q, want the service log", path)
	}
}

// logsTestCommand is the cobra command `logs` runs under: its RunE asks for the
// --ip flag and for a context, which is everything a test has to provide.
func logsTestCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().String("ip", "", "")
	return cmd
}

// withLogsFlags installs one flag combination for the duration of a test. Every
// flag is written here rather than inherited: `logs` binds pflag values straight
// into these package variables, so a test that executes the command leaves them
// set for whatever runs next.
func withLogsFlags(t *testing.T, set func()) {
	t.Helper()
	oldFollow, oldJSON, oldOnce := logsFollow, logsJSONFlag, logsOnceFlag
	oldRun, oldFirst, oldLines := logsRunFlag, logsFirstErr, logsLinesFlag
	t.Cleanup(func() {
		logsFollow, logsJSONFlag, logsOnceFlag = oldFollow, oldJSON, oldOnce
		logsRunFlag, logsFirstErr, logsLinesFlag = oldRun, oldFirst, oldLines
	})
	logsFollow, logsJSONFlag, logsOnceFlag = true, false, false
	logsRunFlag, logsFirstErr, logsLinesFlag = "", false, 10
	set()
}

// demoProject writes one manifest into a fresh directory, moves there, and
// returns that directory.
func demoProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	const manifest = "name: demo\nservices:\n  - name: worker\n    cmd: echo worker\n"
	if err := os.WriteFile(filepath.Join(dir, "oberth.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

// TestLogsOnceReadsTheDeclaredServiceLog: `oberth logs <service> --once` reads
// the log the project points at and prints the file under a header naming it.
func TestLogsOnceReadsTheDeclaredServiceLog(t *testing.T) {
	restore := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = restore })

	demoProject(t)
	logPath := filepath.Join(paths.Logs(), "demo", "worker.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("booting\nready on 3000\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	resetRouting(t)
	noDaemonReachable(t)
	withLogsFlags(t, func() { logsOnceFlag, logsLinesFlag = true, 10 })

	var err error
	out := captureStdout(t, func() { err = logsRun(logsTestCommand(), []string{"worker"}) })
	if err != nil {
		t.Fatalf("logs worker --once: %v", err)
	}
	want := "  " + logPath + "\n\nbooting\nready on 3000\n"
	if out != want {
		t.Fatalf("stdout = %q, want %q", out, want)
	}
}

// TestLogsJSONNeedsTheDaemon: the direct path hands the terminal to tail, so a
// structured answer has to come from the daemon — and says so.
func TestLogsJSONNeedsTheDaemon(t *testing.T) {
	resetRouting(t)
	noDaemonReachable(t)
	withLogsFlags(t, func() { logsJSONFlag = true })

	var err error
	out := captureStdout(t, func() { err = logsRun(logsTestCommand(), []string{"3000"}) })
	if out != "" {
		t.Fatalf("stdout = %q, want nothing", out)
	}
	code, msg, hint := describe(err)
	if code != "daemon_unavailable" || !strings.Contains(msg, "logs --json needs the daemon") {
		t.Fatalf("code = %q msg = %q, want the daemon requirement", code, msg)
	}
	if !strings.Contains(hint, "oberth serve") {
		t.Errorf("hint = %q, want the way to start one", hint)
	}
}

// TestLogsMissingPortFails: a port nobody listens on is a failure, not an empty
// successful read.
func TestLogsMissingPortFails(t *testing.T) {
	resetRouting(t)
	noDaemonReachable(t)
	withLogsFlags(t, func() { logsOnceFlag = true })

	err := logsRun(logsTestCommand(), []string{"1"})
	if err == nil {
		t.Fatal("logs 1 returned nil")
	}
	if got := exitCodeFor(err); got != exitFail {
		t.Fatalf("exit code = %d, want %d", got, exitFail)
	}
}

// TestLogsFirstErrorRequiresARun: without a run scope the file mixes earlier
// generations, so the selector is refused instead of answering from the wrong
// lines.
func TestLogsFirstErrorRequiresARun(t *testing.T) {
	withLogsFlags(t, func() { logsFirstErr = true })

	err := logsRun(logsTestCommand(), []string{"3000"})
	var usage usageError
	if !errors.As(err, &usage) {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if !strings.Contains(err.Error(), "--first-error needs --run current") {
		t.Fatalf("err = %v, want the missing-scope refusal", err)
	}
}

// TestLogsRunCannotBeCombinedWithIP: --ip names a listener and --run names a
// service's run; one invocation cannot be both.
func TestLogsRunCannotBeCombinedWithIP(t *testing.T) {
	withLogsFlags(t, func() { logsRunFlag = "current" })
	cmd := logsTestCommand()
	if err := cmd.Flags().Set("ip", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}

	err := logsRun(cmd, []string{"api"})
	var usage usageError
	if !errors.As(err, &usage) {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("err = %v, want the exclusivity refusal", err)
	}
}

// TestLogsRunNeedsTheDaemon: a run exists only inside the daemon, so the run
// form cannot fall back to the local scan.
func TestLogsRunNeedsTheDaemon(t *testing.T) {
	resetRouting(t)
	noDaemonReachable(t)
	withLogsFlags(t, func() { logsRunFlag = "current" })

	err := logsRun(logsTestCommand(), []string{"api"})
	code, msg, hint := describe(err)
	if code != "daemon_unavailable" || !strings.Contains(msg, "logs --run needs the daemon") {
		t.Fatalf("code = %q msg = %q, want the daemon requirement", code, msg)
	}
	if !strings.Contains(hint, "oberth serve") {
		t.Errorf("hint = %q, want the way to start one", hint)
	}
}

// TestLogsUnknownServiceIsNotFound: a name the project does not declare is
// reported as such, with the project it was looked for in.
func TestLogsUnknownServiceIsNotFound(t *testing.T) {
	demoProject(t)
	resetRouting(t)
	noDaemonReachable(t)
	withLogsFlags(t, func() { logsOnceFlag = true })

	err := logsRun(logsTestCommand(), []string{"nope"})
	code, msg, _ := describe(err)
	if code != "not_found" || !strings.Contains(msg, `no service named "nope" in demo`) {
		t.Fatalf("code = %q msg = %q, want the missing service", code, msg)
	}
}

// TestLogsHelperService is not a test: re-executing the test binary with
// BERTH_TEST_LOG_PORT set gives the daemon a real listener whose output really
// goes to a file, which is what a log read needs.
func TestLogsHelperService(t *testing.T) {
	port := os.Getenv("BERTH_TEST_LOG_PORT")
	if port == "" {
		t.Skip("helper process, driven by TestLogsOnceReadsAListenerThroughTheDaemon")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("helper could not listen: %v", err)
	}
	defer ln.Close()
	fmt.Println("booting")
	fmt.Println("ready on a port")
	time.Sleep(60 * time.Second)
}

// startLoggingService spawns the helper on a free port with its output in a
// real file, and returns that port.
func startLoggingService(t *testing.T) int {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the log-source harness is unix-only")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(t.TempDir(), "service.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestLogsHelperService", "-test.timeout=90s")
	cmd.Env = append(os.Environ(), "BERTH_TEST_LOG_PORT="+portStr)
	cmd.Stdout = logFile
	born := time.Now().UTC()
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the helper: %v", err)
	}
	t.Cleanup(func() {
		_ = killOwnedIdentities(heldKillIdentity(cmd.Process, born))
		_, _ = cmd.Process.Wait()
		logFile.Close()
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", "127.0.0.1:"+portStr, 200*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			registerIsolatedKillFixture(t, ports.ListeningPort{
				Port: port, PID: cmd.Process.Pid, BindAddress: "127.0.0.1",
				Type: ports.PortTypeUser,
			}, heldKillIdentity(cmd.Process, born))
			useIsolatedEnrichedKillScan(t)
			return port
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the helper never listened on %d", port)
	return 0
}

// TestLogsOnceReadsAListenerThroughTheDaemon: one read of a listener's log goes
// to the daemon, which owns the log source, and comes back as one JSON document
// naming that source and carrying its lines.
func TestLogsOnceReadsAListenerThroughTheDaemon(t *testing.T) {
	port := startLoggingService(t)
	resetRouting(t)
	startScanningDaemon(t)
	withLogsFlags(t, func() { logsOnceFlag, logsJSONFlag, logsLinesFlag = true, true, 10 })

	var err error
	out := captureStdout(t, func() { err = logsRun(logsTestCommand(), []string{strconv.Itoa(port)}) })
	if err != nil {
		t.Fatalf("logs %d --once --json: %v", port, err)
	}
	var doc logsDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	if doc.Source == "" {
		t.Fatalf("source = %q, want the file the daemon read", doc.Source)
	}
	if got := strings.Join(doc.Lines, "|"); !strings.Contains(got, "ready on a port") {
		t.Fatalf("lines = %q, want the listener's own output", got)
	}
}

// TestLogsFollowThroughTheDaemonStreamsTheListenersOutput: following prints the
// attaching header and the lines as they arrive. The follow ends when the daemon
// that owned the reader stops; the client delivers that close as an ordinary end
// of stream, so the invocation returns without an error.
func TestLogsFollowThroughTheDaemonStreamsTheListenersOutput(t *testing.T) {
	restore := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = restore })

	port := startLoggingService(t)
	resetRouting(t)
	stopDaemon := startScanningDaemon(t)
	withLogsFlags(t, func() { logsFollow, logsLinesFlag = true, 10 })

	go func() {
		time.Sleep(700 * time.Millisecond)
		stopDaemon()
	}()

	var err error
	out := captureStdout(t, func() { err = logsRun(logsTestCommand(), []string{strconv.Itoa(port)}) })
	if !strings.Contains(out, "Attaching to") {
		t.Fatalf("stdout = %q, want the attaching header", out)
	}
	if !strings.Contains(out, "ready on a port") {
		t.Fatalf("stdout = %q, want the streamed line", out)
	}
	if err != nil {
		t.Fatalf("err = %v, want the daemon's close reported as the end of the stream", err)
	}
}

// TestLogsFollowJSONThroughTheDaemon: in JSON mode the follow is one object per
// line and the attaching header stays off stdout.
func TestLogsFollowJSONThroughTheDaemon(t *testing.T) {
	port := startLoggingService(t)
	resetRouting(t)
	stopDaemon := startScanningDaemon(t)
	withLogsFlags(t, func() { logsFollow, logsJSONFlag, logsLinesFlag = true, true, 10 })

	go func() {
		time.Sleep(700 * time.Millisecond)
		stopDaemon()
	}()

	var err error
	out := captureStdout(t, func() { err = logsRun(logsTestCommand(), []string{strconv.Itoa(port)}) })
	if err != nil {
		t.Fatalf("err = %v, want the daemon's close reported as the end of the stream", err)
	}
	if strings.Contains(out, "Attaching to") {
		t.Fatal("the plain-text header appeared in JSON mode")
	}
	objects := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(line), &doc); err != nil {
			t.Fatalf("line %q is not one JSON object: %v", line, err)
		}
		if doc["source"] == nil || doc["line"] == nil {
			t.Fatalf("line %q is missing source or line", line)
		}
		objects++
	}
	if objects == 0 {
		t.Fatal("the JSON follow printed no objects")
	}
}
