package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// blindObservation makes the collector look like a sandboxed one: it exits
// successfully with no table, and the probe can bind but cannot see itself.
func blindObservation(t *testing.T) {
	t.Helper()
	prevPorts, prevProbe := scanPorts, scanProbe
	scanPorts = func() ([]ports.ListeningPort, error) {
		return nil, &ports.ObservationError{
			Code:   ports.CodeObservationEmpty,
			Detail: "netstat: exited 0 but printed no listening table",
			Hint:   "run `oberth doctor --only listener_observation`",
		}
	}
	scanProbe = func(context.Context) ports.ProbeResult {
		return ports.ProbeResult{
			State: ports.ObservationBlind, Code: ports.CodeObservationBlind,
			Detail: "bound 127.0.0.1:1 but the table did not show it",
			Hint:   "retry outside the sandbox",
		}
	}
	t.Cleanup(func() { scanPorts, scanProbe = prevPorts, prevProbe })
}

func demoManifest(t *testing.T) string {
	t.Helper()
	t.Setenv("BERTH_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, groups.ConfigName),
		[]byte("name: demo\nservices:\n  - name: api\n    cmd: echo api\n    port: 19001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	return dir
}

// A blind collector must not erase the declaration: the manifest and run
// registry still say what was declared, while ports stay unknown.
func TestScanGroupsObservedKeepsManifestWhenBlind(t *testing.T) {
	demoManifest(t)
	blindObservation(t)

	_, gg, obs, err := scanGroupsObserved(context.Background())
	if err != nil {
		t.Fatalf("degraded scan returned an error: %v", err)
	}
	if obs == nil || obs.State != string(ports.ObservationBlind) || obs.Code != ports.CodeObservationBlind {
		t.Fatalf("observation = %+v", obs)
	}
	if len(gg) != 1 || gg[0].Name != "demo" {
		t.Fatalf("groups = %+v, want the demo manifest", gg)
	}
	api := serviceIn(gg[0], "api")
	if api == nil {
		t.Fatalf("manifest service lost: %+v", gg[0].Services)
	}
	if api.PortActual != nil {
		t.Fatalf("port_actual = %v, must stay unknown", *api.PortActual)
	}
}

// A partial table keeps its readable rows; the observation says the rest is
// unknown rather than empty.
func TestScanGroupsObservedKeepsReadableRowsWhenPartial(t *testing.T) {
	dir := demoManifest(t)
	readable := ports.ListeningPort{
		Port: 18123, PID: os.Getpid(), BindAddress: "0.0.0.0", IPVersion: "IPv4", Cwd: dir,
	}
	prevPorts := scanPorts
	scanPorts = func() ([]ports.ListeningPort, error) {
		return []ports.ListeningPort{readable}, &ports.ObservationError{
			Code: ports.CodeObservationIncomplete, Detail: "netstat: unreadable TCP rows: line 3",
			Hint: "run doctor", Partial: true, Rows: []ports.ListeningPort{readable},
		}
	}
	t.Cleanup(func() { scanPorts = prevPorts })

	got, _, obs, err := scanGroupsObserved(context.Background())
	if err != nil {
		t.Fatalf("partial scan returned an error: %v", err)
	}
	if len(got) != 1 || got[0].Port != 18123 {
		t.Fatalf("rows = %+v, want the readable row", got)
	}
	if obs == nil || obs.State != "partial" || obs.Code != ports.CodeObservationIncomplete {
		t.Fatalf("observation = %+v", obs)
	}
}

func TestStatusDocumentKeepsManifestWhenBlind(t *testing.T) {
	dir := demoManifest(t)
	prev := noDaemonFlag
	noDaemonFlag = true
	t.Cleanup(func() { noDaemonFlag = prev })
	blindObservation(t)

	doc, err := buildStatusDocument(context.Background(), dir, false)
	if err != nil {
		t.Fatalf("status failed instead of degrading: %v", err)
	}
	if doc.Observation == nil || doc.Observation.State != string(ports.ObservationBlind) {
		t.Fatalf("observation = %+v", doc.Observation)
	}
	if doc.Worktree.Name != "demo" {
		t.Fatalf("worktree = %q, want demo", doc.Worktree.Name)
	}
	if len(doc.Ports) != 0 {
		t.Fatalf("ports = %+v, want none", doc.Ports)
	}
	if len(doc.Worktree.Services) != 1 || doc.Worktree.Services[0].Name != "api" {
		t.Fatalf("services = %+v", doc.Worktree.Services)
	}
}

// Readiness must not wait out its budget, and must not authorize stopping the
// run it just started, when the process cannot see listeners at all.
func TestWaitForStartReadyFailsFastWhenBlind(t *testing.T) {
	dir := demoManifest(t)
	prev := noDaemonFlag
	noDaemonFlag = true
	t.Cleanup(func() { noDaemonFlag = prev })
	blindObservation(t)

	path := filepath.Join(dir, groups.ConfigName)
	chunks := []rpc.GroupsStartChunk{{Service: "api", State: "started"}}
	summary := rpc.GroupsStartEnd{Started: []string{"api"}}
	started := time.Now()
	cleanup, err := waitForStartReady(context.Background(),
		rpc.GroupsStartParams{ConfigPath: &path}, nil, chunks, &summary, 5*time.Second, true)
	if err == nil {
		t.Fatal("a blind process must not claim readiness")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("waited %s for a blind observation", elapsed)
	}
	if len(cleanup) != 0 {
		t.Fatalf("blindness authorized cleanup: %v", cleanup)
	}
	code, _, hint := describe(err)
	if code != ports.CodeObservationBlind || hint == "" {
		t.Fatalf("error = %q / %q, want observation_blind with a hint", code, hint)
	}
	if chunks[0].State != "started" || len(summary.Errors) != 0 {
		t.Fatalf("blindness was recorded as a readiness timeout: %+v / %+v", chunks, summary)
	}
}
