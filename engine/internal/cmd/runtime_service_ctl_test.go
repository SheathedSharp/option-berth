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

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func syntheticRuntimeServiceStatus(observation *rpc.ObservationStatus) runtimeServiceStatus {
	return runtimeServiceStatus{
		status: rpc.RuntimeServiceStatusResult{
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
		hello: rpc.RuntimeServiceHelloResult{
			Socket:                "/tmp/berth.sock",
			RuntimeServiceVersion: "v1.2.3",
			ProtocolVersion:       "1",
			Capabilities:          []string{"groups.start"},
		},
		cliCommit: "deadbee",
	}
}

func TestRuntimeServiceStatusText(t *testing.T) {
	doc := syntheticRuntimeServiceStatus(&rpc.ObservationStatus{
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

func TestRuntimeServiceStatusTextOmitsAnObservableObservation(t *testing.T) {
	var buf bytes.Buffer
	syntheticRuntimeServiceStatus(&rpc.ObservationStatus{
		State: state.ObservationObservable,
		Code:  "",
	}).writeText(&buf)
	if strings.Contains(buf.String(), "observation") {
		t.Fatalf("an observable runtime service needs no observation row:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "database      /tmp/berth.db") {
		t.Fatalf("the database row disappeared:\n%s", buf.String())
	}
}

func TestRuntimeServiceStatusJSONDocKeepsItsDocumentedFields(t *testing.T) {
	body := syntheticRuntimeServiceStatus(nil).jsonDoc()
	want := map[string]any{
		"running":                 true,
		"pid":                     float64(42),
		"uptime":                  "3m12s",
		"subscribers":             float64(2),
		"last_scan_at":            "2026-11-09T10:00:00Z",
		"scan_interval_ms":        float64(1000),
		"scan_base_interval_ms":   float64(500),
		"stats_interval_ms":       float64(2000),
		"scans":                   float64(7),
		"db_path":                 "/tmp/berth.db",
		"socket":                  "/tmp/berth.sock",
		"runtime_service_version": "v1.2.3",
		"protocol_version":        "1",
		"capabilities":            []any{"groups.start"},
		"build_commit":            "deadbee",
		"build_date":              "2026-11-09T09:00:00Z",
		"build_matches":           true,
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
		t.Error("observation must be absent when the runtime service did not report one")
	}
}

func TestRuntimeServiceStatusJSONCarriesObservationAndMismatchedBuild(t *testing.T) {
	doc := syntheticRuntimeServiceStatus(&rpc.ObservationStatus{State: string(ports.ObservationBlind), Code: ports.CodeObservationBlind})
	doc.cliCommit = "cafebabe"
	body := doc.jsonDoc()
	if body["build_matches"] != false {
		t.Errorf("build_matches = %v, want false for two different commits", body["build_matches"])
	}
	if body["observation"] == nil {
		t.Error("the runtime service's observation verdict must travel with the status")
	}
}

func TestRuntimeServiceStatusTreatsAnUnknownBuildCommitAsAMatch(t *testing.T) {
	doc := syntheticRuntimeServiceStatus(nil)
	doc.status.Commit = ""
	if body := doc.jsonDoc(); body["build_matches"] != true {
		t.Errorf("build_matches = %v, want true when the runtime service cannot name its build", body["build_matches"])
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

// shortSocketPath returns a unix socket path under a short temp directory.
// t.TempDir() includes the test name, and macOS rejects socket paths over
// roughly 104 bytes; the CI temp root plus a long test name overflows it.
func shortSocketPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sn")
	if err != nil {
		t.Fatalf("socket temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}

// isolateRuntimeServicePaths gives this test its own state directory, database and
// socket, so a runtime service started here is never the one another test is looking
// for and its ledger never names another test's checkout. The database needs
// its own variable: $BERTH_DB outranks $BERTH_HOME for the store.
func isolateRuntimeServicePaths(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	t.Setenv("BERTH_DB", filepath.Join(home, "option-berth.db"))
	socket := shortSocketPath(t, "runtime-service.sock")
	t.Setenv("BERTH_RUNTIME_SERVICE_SOCKET", socket)
	return socket
}

// runRuntimeServiceAt serves a real runtime service on socket until the test ends. Its scan is
// fixed and empty: these tests are about the command's own routing and output,
// not about what the machine happens to be listening on.
func runRuntimeServiceAt(t *testing.T, socket string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket harness does not apply to named pipes")
	}
	srv := runtimesvc.New(runtimesvc.Options{
		Socket:  socket,
		Version: "test",
		Scanner: scanner.New(scanner.Options{
			RuntimeServiceVersion: "test",
			Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
				return nil, nil
			},
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	if err := client.WaitForSocket(ctx, socket, 5*time.Second); err != nil {
		cancel()
		t.Fatalf("runtime service did not come up: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		<-srv.Done()
	})
}

// startRuntimeServiceWhereTheCommandLooks runs a real runtime service on the socket `oberth
// runtime service` looks at. The command's own dial policy — dial the running runtime service and
// never start one — is then what the test exercises end to end.
func startRuntimeServiceWhereTheCommandLooks(t *testing.T) string {
	t.Helper()
	socket := isolateRuntimeServicePaths(t)
	runRuntimeServiceAt(t, socket)
	return socket
}

// setRuntimeServiceJSONFlag installs --json for one test.
func setRuntimeServiceJSONFlag(t *testing.T, asJSON bool) {
	t.Helper()
	prev := runtimeServiceJSONFlag
	runtimeServiceJSONFlag = asJSON
	t.Cleanup(func() { runtimeServiceJSONFlag = prev })
}

// TestRuntimeServiceStatusDescribesTheRunningRuntimeService: the text answer is the runtime service's
// own status, read over the socket the command dials — including the pid of the
// process that is serving it and the socket a reader would connect to.
func TestRuntimeServiceStatusDescribesTheRunningRuntimeService(t *testing.T) {
	socket := startRuntimeServiceWhereTheCommandLooks(t)
	setRuntimeServiceJSONFlag(t, false)

	var err error
	out := captureStdout(t, func() { err = runtimeServiceStatusRun(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runtime service status: %v", err)
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

// TestRuntimeServiceStatusJSONIsOneDocumentAboutTheSameRuntimeService checks the --json keys
// against the live runtime_service: one JSON value on stdout, running, with its pid and
// socket, and a build that matches the CLI's own.
func TestRuntimeServiceStatusJSONIsOneDocumentAboutTheSameRuntimeService(t *testing.T) {
	socket := startRuntimeServiceWhereTheCommandLooks(t)
	setRuntimeServiceJSONFlag(t, true)

	var err error
	out := captureStdout(t, func() { err = runtimeServiceStatusRun(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runtime service status --json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	if doc["running"] != true {
		t.Errorf("running = %v, want true", doc["running"])
	}
	if doc["pid"] != float64(os.Getpid()) {
		t.Errorf("pid = %v, want the runtime service's own pid %d", doc["pid"], os.Getpid())
	}
	if doc["socket"] != socket {
		t.Errorf("socket = %v, want %q", doc["socket"], socket)
	}
	if doc["runtime_service_version"] != "test" {
		t.Errorf("runtime_service_version = %v, want test", doc["runtime_service_version"])
	}
	if doc["protocol_version"] == "" || doc["protocol_version"] == nil {
		t.Errorf("protocol_version = %v, want the negotiated one", doc["protocol_version"])
	}
	if doc["build_matches"] != true {
		t.Errorf("build_matches = %v, want true for one binary", doc["build_matches"])
	}
}

// TestRuntimeServiceStatusOfflineIsTheNotRunningAnswer: with no runtime service the command says
// so as an error with the documented code, and prints nothing on stdout.
func TestRuntimeServiceStatusOfflineIsTheNotRunningAnswer(t *testing.T) {
	isolateRuntimeServicePaths(t)
	setRuntimeServiceJSONFlag(t, false)

	var err error
	out := captureStdout(t, func() { err = runtimeServiceStatusRun(testCommand(), nil) })
	if out != "" {
		t.Fatalf("stdout = %q, want nothing in text mode", out)
	}
	code, _, hint := describe(err)
	if code != "runtime_service_not_running" {
		t.Fatalf("code = %q (%v), want runtime_service_not_running", code, err)
	}
	if !strings.Contains(hint, "oberth runtime service run") {
		t.Errorf("hint = %q, want the way to start one", hint)
	}
}

// TestRuntimeServiceStatusOfflineJSONStillAnswers: `runtime service status --json` documents
// {"running": false} on stdout as an answer, with a quiet non-zero exit rather
// than an error line.
func TestRuntimeServiceStatusOfflineJSONStillAnswers(t *testing.T) {
	isolateRuntimeServicePaths(t)
	setRuntimeServiceJSONFlag(t, true)

	var err error
	out := captureStdout(t, func() { err = runtimeServiceStatusRun(testCommand(), nil) })
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

// TestRuntimeServiceStopStopsTheRuntimeServiceItFound: the stop waits until the socket is gone,
// so a caller that runs `runtime service run` next cannot race the previous runtime.service.
func TestRuntimeServiceStopStopsTheRuntimeServiceItFound(t *testing.T) {
	socket := startRuntimeServiceWhereTheCommandLooks(t)
	setRuntimeServiceJSONFlag(t, false)

	var err error
	out := captureStdout(t, func() { err = runtimeServiceStopRun(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runtime service stop: %v", err)
	}
	if !strings.Contains(out, "oberth runtime service stopped") {
		t.Fatalf("stdout = %q, want the stopped sentence", out)
	}
	if runtimesvc.SocketAlive(socket) {
		t.Fatal("the socket is still alive after runtime service stop returned")
	}
}

// TestRuntimeServiceStopJSONReportsTheSocket: the JSON form answers {stopped, socket}.
func TestRuntimeServiceStopJSONReportsTheSocket(t *testing.T) {
	socket := startRuntimeServiceWhereTheCommandLooks(t)
	setRuntimeServiceJSONFlag(t, true)

	var err error
	out := captureStdout(t, func() { err = runtimeServiceStopRun(testCommand(), nil) })
	if err != nil {
		t.Fatalf("runtime service stop --json: %v", err)
	}
	var doc map[string]any
	if e := json.Unmarshal([]byte(out), &doc); e != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", e, out)
	}
	if doc["stopped"] != true || doc["socket"] != socket {
		t.Fatalf("document = %v, want stopped true and socket %q", doc, socket)
	}
}
