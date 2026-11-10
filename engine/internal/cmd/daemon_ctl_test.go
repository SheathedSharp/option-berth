package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func syntheticDaemonStatus(observation *rpc.ObservationStatus) daemonStatus {
	return daemonStatus{
		status: rpc.DaemonStatusResult{
			PID:                42,
			Uptime:             "3m12s",
			Subscribers:        2,
			LastScanAt:         "2026-11-09T10:00:00Z",
			Commit:             "deadbee",
			Built:              "2026-11-09T09:00:00Z",
			ScanIntervalMs:     1000,
			ScanBaseIntervalMs: 500,
			StatsIntervalMs:    2000,
			Scans:              7,
			DBPath:             "/tmp/berth.db",
			Observation:        observation,
		},
		hello: rpc.DaemonHelloResult{
			Socket:          "/tmp/berth.sock",
			DaemonVersion:   "v1.2.3",
			ProtocolVersion: "1",
			Capabilities:    []string{"groups.start"},
		},
		cliCommit: "deadbee",
	}
}

func TestDaemonStatusText(t *testing.T) {
	doc := syntheticDaemonStatus(&rpc.ObservationStatus{
		State: state.ObservationPartial,
		Code:  ports.CodeObservationIncomplete,
		Hint:  "retry outside the sandbox",
	})
	var buf bytes.Buffer
	doc.writeText(&buf)
	const want = `running       yes
pid           42
version       v1.2.3 (protocol 1)
build         deadbee, built 2026-11-09T09:00:00Z
uptime        3m12s
subscribers   2
scan interval 1000ms
scan base     500ms
stats tick    2000ms
scans         7
last scan     2026-11-09T10:00:00Z
socket        /tmp/berth.sock
observation   partial (observation_incomplete)
hint          retry outside the sandbox
database      /tmp/berth.db
capabilities  [groups.start]
`
	if got := buf.String(); got != want {
		t.Fatalf("text output:\n%s\nwant:\n%s", got, want)
	}
}

func TestDaemonStatusTextOmitsAnObservableObservation(t *testing.T) {
	var buf bytes.Buffer
	syntheticDaemonStatus(&rpc.ObservationStatus{
		State: state.ObservationObservable,
		Code:  "",
	}).writeText(&buf)
	if strings.Contains(buf.String(), "observation") {
		t.Fatalf("an observable daemon needs no observation row:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "database      /tmp/berth.db") {
		t.Fatalf("the database row disappeared:\n%s", buf.String())
	}
}

func TestDaemonStatusJSONDocKeepsItsDocumentedFields(t *testing.T) {
	body := syntheticDaemonStatus(nil).jsonDoc()
	want := map[string]any{
		"running":               true,
		"pid":                   float64(42),
		"uptime":                "3m12s",
		"subscribers":           float64(2),
		"last_scan_at":          "2026-11-09T10:00:00Z",
		"scan_interval_ms":      float64(1000),
		"scan_base_interval_ms": float64(500),
		"stats_interval_ms":     float64(2000),
		"scans":                 float64(7),
		"db_path":               "/tmp/berth.db",
		"socket":                "/tmp/berth.sock",
		"daemon_version":        "v1.2.3",
		"protocol_version":      "1",
		"capabilities":          []any{"groups.start"},
		"build_commit":          "deadbee",
		"build_date":            "2026-11-09T09:00:00Z",
		"build_matches":         true,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for key, wantValue := range want {
		gotValue, ok := got[key]
		if !ok {
			t.Errorf("JSON document is missing %q", key)
			continue
		}
		if left, right := mustJSON(t, gotValue), mustJSON(t, wantValue); left != right {
			t.Errorf("%s = %s, want %s", key, left, right)
		}
	}
	if _, ok := got["observation"]; ok {
		t.Error("observation must be absent when the daemon did not report one")
	}
}

func TestDaemonStatusJSONCarriesObservationAndMismatchedBuild(t *testing.T) {
	doc := syntheticDaemonStatus(&rpc.ObservationStatus{State: string(ports.ObservationBlind), Code: ports.CodeObservationBlind})
	doc.cliCommit = "cafebabe"
	body := doc.jsonDoc()
	if body["build_matches"] != false {
		t.Errorf("build_matches = %v, want false for two different commits", body["build_matches"])
	}
	if body["observation"] == nil {
		t.Error("the daemon's observation verdict must travel with the status")
	}
}

func TestDaemonStatusTreatsAnUnknownBuildCommitAsAMatch(t *testing.T) {
	doc := syntheticDaemonStatus(nil)
	doc.status.Commit = ""
	if body := doc.jsonDoc(); body["build_matches"] != true {
		t.Errorf("build_matches = %v, want true when the daemon cannot name its build", body["build_matches"])
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// isolateDaemonPaths gives this test its own state directory, database and
// socket, so a daemon started here is never the one another test is looking
// for and its ledger never names another test's checkout. The database needs
// its own variable: $BERTH_DB outranks $BERTH_HOME for the store.
func isolateDaemonPaths(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	t.Setenv("BERTH_DB", filepath.Join(home, "option-berth.db"))
	socket := filepath.Join(t.TempDir(), "daemon.sock")
	t.Setenv("BERTH_SOCKET", socket)
	return socket
}

// runDaemonAt serves a real daemon on socket until the test ends. Its scan is
// fixed and empty: these tests are about the command's own routing and output,
// not about what the machine happens to be listening on.
func runDaemonAt(t *testing.T, socket string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket harness does not apply to named pipes")
	}
	srv := daemon.New(daemon.Options{
		Socket:  socket,
		Version: "test",
		Scanner: scanner.New(scanner.Options{
			DaemonVersion: "test",
			Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
				return nil, nil
			},
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	if err := client.WaitForSocket(ctx, socket, 5*time.Second); err != nil {
		cancel()
		t.Fatalf("daemon did not come up: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		<-srv.Done()
	})
}

// startDaemonWhereTheCommandLooks runs a real daemon on the socket `oberth
// daemon` looks at. The command's own dial policy — dial the running daemon and
// never start one — is then what the test exercises end to end.
func startDaemonWhereTheCommandLooks(t *testing.T) string {
	t.Helper()
	socket := isolateDaemonPaths(t)
	runDaemonAt(t, socket)
	return socket
}

// setDaemonJSONFlag installs --json for one test.
func setDaemonJSONFlag(t *testing.T, asJSON bool) {
	t.Helper()
	prev := daemonJSONFlag
	daemonJSONFlag = asJSON
	t.Cleanup(func() { daemonJSONFlag = prev })
}

// TestDaemonStatusDescribesTheRunningDaemon: the text answer is the daemon's
// own status, read over the socket the command dials — including the pid of the
// process that is serving it and the socket a reader would connect to.
func TestDaemonStatusDescribesTheRunningDaemon(t *testing.T) {
	socket := startDaemonWhereTheCommandLooks(t)
	setDaemonJSONFlag(t, false)

	var err error
	out := captureStdout(t, func() { err = daemonStatusRun(testCommand(), nil) })
	if err != nil {
		t.Fatalf("daemon status: %v", err)
	}
	for _, want := range []string{
		"running       yes",
		fmt.Sprintf("pid           %d", os.Getpid()),
		"version       test (protocol ",
		"socket        " + socket,
		"capabilities  [",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output is missing %q:\n%s", want, out)
		}
	}
}

// TestDaemonStatusJSONIsOneDocumentAboutTheSameDaemon checks the --json keys
// against the live daemon: one JSON value on stdout, running, with its pid and
// socket, and a build that matches the CLI's own.
func TestDaemonStatusJSONIsOneDocumentAboutTheSameDaemon(t *testing.T) {
	socket := startDaemonWhereTheCommandLooks(t)
	setDaemonJSONFlag(t, true)

	var err error
	out := captureStdout(t, func() { err = daemonStatusRun(testCommand(), nil) })
	if err != nil {
		t.Fatalf("daemon status --json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	if doc["running"] != true {
		t.Errorf("running = %v, want true", doc["running"])
	}
	if doc["pid"] != float64(os.Getpid()) {
		t.Errorf("pid = %v, want the daemon's own pid %d", doc["pid"], os.Getpid())
	}
	if doc["socket"] != socket {
		t.Errorf("socket = %v, want %q", doc["socket"], socket)
	}
	if doc["daemon_version"] != "test" {
		t.Errorf("daemon_version = %v, want test", doc["daemon_version"])
	}
	if doc["protocol_version"] == "" || doc["protocol_version"] == nil {
		t.Errorf("protocol_version = %v, want the negotiated one", doc["protocol_version"])
	}
	if doc["build_matches"] != true {
		t.Errorf("build_matches = %v, want true for one binary", doc["build_matches"])
	}
}

// TestDaemonStatusOfflineIsTheNotRunningAnswer: with no daemon the command says
// so as an error with the documented code, and prints nothing on stdout.
func TestDaemonStatusOfflineIsTheNotRunningAnswer(t *testing.T) {
	isolateDaemonPaths(t)
	setDaemonJSONFlag(t, false)

	var err error
	out := captureStdout(t, func() { err = daemonStatusRun(testCommand(), nil) })
	if out != "" {
		t.Fatalf("stdout = %q, want nothing in text mode", out)
	}
	code, _, hint := describe(err)
	if code != "daemon_not_running" {
		t.Fatalf("code = %q (%v), want daemon_not_running", code, err)
	}
	if !strings.Contains(hint, "oberth serve") {
		t.Errorf("hint = %q, want the way to start one", hint)
	}
}

// TestDaemonStatusOfflineJSONStillAnswers: `daemon status --json` documents
// {"running": false} on stdout as an answer, with a quiet non-zero exit rather
// than an error line.
func TestDaemonStatusOfflineJSONStillAnswers(t *testing.T) {
	isolateDaemonPaths(t)
	setDaemonJSONFlag(t, true)

	var err error
	out := captureStdout(t, func() { err = daemonStatusRun(testCommand(), nil) })
	if err != errSilent {
		t.Fatalf("err = %v, want the quiet exit", err)
	}
	var doc map[string]any
	if e := json.Unmarshal([]byte(out), &doc); e != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", e, out)
	}
	if doc["running"] != false {
		t.Errorf("running = %v, want false", doc["running"])
	}
	if doc["socket"] == "" || doc["socket"] == nil {
		t.Errorf("socket = %v, want the socket that was looked at", doc["socket"])
	}
}

// TestDaemonStopStopsTheDaemonItFound: the stop waits until the socket is gone,
// so a caller that runs `serve` next cannot race the previous daemon.
func TestDaemonStopStopsTheDaemonItFound(t *testing.T) {
	socket := startDaemonWhereTheCommandLooks(t)
	setDaemonJSONFlag(t, false)

	var err error
	out := captureStdout(t, func() { err = daemonStopRun(testCommand(), nil) })
	if err != nil {
		t.Fatalf("daemon stop: %v", err)
	}
	if !strings.Contains(out, "oberth daemon stopped") {
		t.Fatalf("stdout = %q, want the stopped sentence", out)
	}
	if daemon.SocketAlive(socket) {
		t.Fatal("the socket is still alive after daemon stop returned")
	}
}

// TestDaemonStopJSONReportsTheSocket: the JSON form answers {stopped, socket}.
func TestDaemonStopJSONReportsTheSocket(t *testing.T) {
	socket := startDaemonWhereTheCommandLooks(t)
	setDaemonJSONFlag(t, true)

	var err error
	out := captureStdout(t, func() { err = daemonStopRun(testCommand(), nil) })
	if err != nil {
		t.Fatalf("daemon stop --json: %v", err)
	}
	var doc map[string]any
	if e := json.Unmarshal([]byte(out), &doc); e != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", e, out)
	}
	if doc["stopped"] != true || doc["socket"] != socket {
		t.Fatalf("document = %v, want stopped true and socket %q", doc, socket)
	}
}
