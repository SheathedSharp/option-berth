package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
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
