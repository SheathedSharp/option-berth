package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// observableProbe keeps readiness tests focused on their own timing rules; the
// real probe depends on the host being able to read its listener table.
func observableProbe(t *testing.T) {
	t.Helper()
	prev := scanProbe
	scanProbe = func(context.Context) ports.ProbeResult {
		return ports.ProbeResult{State: ports.ObservationObservable}
	}
	t.Cleanup(func() { scanProbe = prev })
}

// chdir moves into dir for the duration of a test.
func chdir(t *testing.T, dir string) {
	t.Helper()
	was, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(was) })
}

func TestNormalizeStartChunkAddsStructuredFailure(t *testing.T) {
	c := rpc.GroupsStartChunk{Service: "api", Error: "timed out waiting for db on port 5432"}
	normalizeStartChunk(&c)
	if c.State != "failed" || c.Reason != "dependency_timeout" || c.Hint != "oberth logs api --once" {
		t.Fatalf("chunk = %+v, want structured timeout and hint", c)
	}
}

// A daemon that sends the structured outcome fields also decides its own next
// step, so the compatibility projection must not put a log hint back on a
// service that never started.
func TestNormalizeStartChunkLeavesAStructuredChunkAlone(t *testing.T) {
	const refusal = "pass allow_outside_home: true to start commands outside your home directory"
	refused := rpc.GroupsStartChunk{
		Service: "api", State: "failed", Reason: "start_failed",
		Error: "/tmp/project is outside /Users/someone", Hint: refusal,
	}
	normalizeStartChunk(&refused)
	if refused.Hint != refusal {
		t.Fatalf("hint = %q, want the refusal's own hint", refused.Hint)
	}

	noHint := rpc.GroupsStartChunk{
		Service: "api", State: "failed", Reason: "start_failed",
		Error: "service api has no cmd to run",
	}
	normalizeStartChunk(&noHint)
	if noHint.Hint != "" {
		t.Fatalf("hint = %q, want no hint for a service that never started", noHint.Hint)
	}
}

func TestPrintStartChunkShowsFailureHint(t *testing.T) {
	out := captureStdout(t, func() {
		printStartChunk(rpc.GroupsStartChunk{
			Service: "api", State: "failed", Error: "command not found", Hint: "oberth logs api --once",
		})
	})
	if !strings.Contains(out, "api") || !strings.Contains(out, "oberth logs api --once") {
		t.Fatalf("start output = %q, want service and direct log hint", out)
	}
}

func TestServiceReadyWaitsForConfiguredHealth(t *testing.T) {
	port := 18080
	path := "/healthz"
	group := state.Group{Services: []state.Service{{Name: "api", Running: true, PortActual: &port, Health: &path}}}
	rows := []state.Port{{Port: port, Health: &state.Health{Status: state.HealthFail, Reason: "refused"}}}
	if ready, reason := serviceReady(group, rows, "api"); ready || reason != "health_refused" {
		t.Fatalf("serviceReady = %v, %q, want a health wait", ready, reason)
	}
	rows[0].Health.Status = state.HealthOK
	if ready, reason := serviceReady(group, rows, "api"); !ready || reason != "" {
		t.Fatalf("serviceReady healthy = %v, %q, want ready", ready, reason)
	}
}

func TestServiceReadyTreatsPortlessRunAsReady(t *testing.T) {
	group := state.Group{Services: []state.Service{{Name: "worker", Running: true}}}
	if ready, reason := serviceReady(group, nil, "worker"); !ready || reason != "" {
		t.Fatalf("serviceReady worker = %v, %q, want ready", ready, reason)
	}
}

func TestServiceReadyWaitsForCommandHealthWithoutPort(t *testing.T) {
	health := "command:pg_isready -p 5432"
	group := state.Group{Services: []state.Service{{Name: "db", Running: true, Health: &health}}}
	if ready, reason := serviceReady(group, nil, "db"); ready || reason != "waiting_for_health" {
		t.Fatalf("serviceReady before a verdict = %v, %q, want a health wait", ready, reason)
	}
	group.Services[0].HealthStatus = &state.Health{Status: state.HealthOK}
	if ready, reason := serviceReady(group, nil, "db"); !ready || reason != "" {
		t.Fatalf("serviceReady healthy = %v, %q, want ready", ready, reason)
	}
	group.Services[0].HealthStatus = &state.Health{Status: state.HealthFail, Reason: "command-failed"}
	if ready, reason := serviceReady(group, nil, "db"); ready || reason != "health_command-failed" {
		t.Fatalf("serviceReady failed = %v, %q, want the command verdict", ready, reason)
	}
}

func TestServiceReadyWaitsForListenerWithoutHealth(t *testing.T) {
	port := 18080
	group := state.Group{Services: []state.Service{{Name: "api", Running: true, Port: &port}}}
	if ready, reason := serviceReady(group, nil, "api"); ready || reason != "waiting_for_listener" {
		t.Fatalf("serviceReady before bind = %v, %q, want listener wait", ready, reason)
	}
	group.Services[0].PortActual = &port
	if ready, reason := serviceReady(group, nil, "api"); !ready || reason != "" {
		t.Fatalf("serviceReady after bind = %v, %q, want ready", ready, reason)
	}
}

func TestWaitForStartReadyUsesTimeoutAsStructuredFailure(t *testing.T) {
	observableProbe(t)
	t.Setenv("BERTH_HOME", t.TempDir())
	old := noDaemonFlag
	noDaemonFlag = true
	t.Cleanup(func() { noDaemonFlag = old })
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, groups.ConfigName), []byte("name: demo\nservices:\n  - name: api\n    cmd: echo api\n    port: 19001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	path := filepath.Join(dir, groups.ConfigName)
	params := rpc.GroupsStartParams{ConfigPath: &path}
	chunks := []rpc.GroupsStartChunk{{Service: "api", State: "started"}}
	summary := rpc.GroupsStartEnd{Started: []string{"api"}, Errors: []string{}}
	if timedOut, err := waitForStartReady(context.Background(), params, nil, chunks, &summary, time.Millisecond, true); err != nil {
		t.Fatal(err)
	} else if len(timedOut) != 1 || timedOut[0] != "api" {
		t.Fatalf("timed out = %v, want api", timedOut)
	}
	if chunks[0].State != "failed" || chunks[0].Reason != "ready_timeout" ||
		chunks[0].Hint != "oberth logs api --once" || len(summary.Errors) != 1 {
		t.Fatalf("chunks = %+v, summary = %+v, want structured readiness timeout", chunks, summary)
	}
}

// TestExitAfterTreatsAPreWaitExitAsStale: the exit `down` recorded before an
// `up --wait` began is the previous run's business. Reading it as this run's
// failure flunked every wait that followed a stop.
func TestExitAfterTreatsAPreWaitExitAsStale(t *testing.T) {
	waitStarted := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	stale := &state.ServiceExit{Code: 143, Reason: "stopped", At: waitStarted.Add(-time.Minute).Format(time.RFC3339)}
	fresh := &state.ServiceExit{Code: 1, Reason: "crashed", At: waitStarted.Add(time.Second).Format(time.RFC3339)}
	if exitAfter(stale, waitStarted) {
		t.Fatal("an exit recorded before the wait began is the previous run's, not this one's")
	}
	if !exitAfter(fresh, waitStarted) {
		t.Fatal("an exit recorded after the wait began is this run's death")
	}
	if exitAfter(nil, waitStarted) {
		t.Fatal("a nil exit is no evidence")
	}
	if !exitAfter(&state.ServiceExit{Reason: "crashed"}, waitStarted) {
		t.Fatal("an unparseable timestamp is treated as fresh, not as stale")
	}
}

// TestSelectedExitMatchesTheRunNotTheClock: the run id is the evidence. An
// exit recorded before the RPC returned still belongs to this start when it
// names the selected run; an exit from another run is stale even inside the
// same second.
func TestSelectedExitMatchesTheRunNotTheClock(t *testing.T) {
	since := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	before := &state.ServiceExit{Code: 1, Reason: "crashed", RunID: "run-1",
		At: since.Add(-time.Hour).Format(time.RFC3339)}
	if !selectedExit(before, "run-1", since) {
		t.Fatal("an exit of the selected run must count even if it predates the wait")
	}
	other := &state.ServiceExit{Code: 1, Reason: "crashed", RunID: "run-0",
		At: since.Add(time.Second).Format(time.RFC3339)}
	if selectedExit(other, "run-1", since) {
		t.Fatal("an exit from another run must not be read as this run's death")
	}
	legacy := &state.ServiceExit{Code: 1, Reason: "crashed", At: since.Format(time.RFC3339)}
	if !selectedExit(legacy, "run-1", since) {
		t.Fatal("a record without a run id must fall back to the timestamp")
	}
	if !selectedExit(legacy, "", since) {
		t.Fatal("an exit in the wait-start second must count")
	}
	if selectedExit(nil, "run-1", since) {
		t.Fatal("a nil exit is no evidence")
	}
}

func TestStartChunkReadinessAnnotation(t *testing.T) {
	chunks := []rpc.GroupsStartChunk{{Service: "api", State: "started"}}
	markStartChunkUnconfirmed(&chunks[0])
	if chunks[0].Ready == nil || *chunks[0].Ready {
		t.Fatalf("ready = %v, want an explicit false", chunks[0].Ready)
	}
	data, err := json.Marshal(chunks[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"ready":false`) {
		t.Fatalf("json = %s, want ready:false", data)
	}

	markStartChunkReady(chunks, "api")
	if chunks[0].Ready == nil || !*chunks[0].Ready {
		t.Fatalf("ready = %v, want true after a confirmed wait", chunks[0].Ready)
	}

	summary := rpc.GroupsStartEnd{}
	markStartChunkFailed(chunks, &summary, "api", "crashed", "boom", nil)
	if chunks[0].Ready != nil {
		t.Fatalf("ready = %v, want none on a failed chunk", chunks[0].Ready)
	}
}

func TestPrintStartChunkShowsUnconfirmedReadiness(t *testing.T) {
	unconfirmed := false
	out := captureStdout(t, func() {
		printStartChunk(rpc.GroupsStartChunk{
			Service: "api", State: "started", Ready: &unconfirmed, PID: 1, LogPath: "/tmp/api.log",
		})
	})
	if !strings.Contains(out, "readiness not confirmed") || strings.Contains(out, "✓") {
		t.Fatalf("unconfirmed output = %q, want a distinct marker and wording", out)
	}

	confirmed := true
	out = captureStdout(t, func() {
		printStartChunk(rpc.GroupsStartChunk{
			Service: "api", State: "started", Ready: &confirmed, PID: 1, LogPath: "/tmp/api.log",
		})
	})
	if !strings.Contains(out, "✓") || strings.Contains(out, "readiness not confirmed") {
		t.Fatalf("confirmed output = %q, want the ready check", out)
	}
}

// TestUpParamsUsesTheConfigInTheWorkingDirectory: with no argument, `oberth up`
// starts the project you are standing in.
func TestUpParamsUsesTheConfigInTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, groups.ConfigName),
		[]byte("name: demo\nservices:\n  - name: api\n    cmd: uv run api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)

	upOnly = nil
	upAllowOutsideHome = false
	params, _, err := upParams(nil)
	if err != nil {
		t.Fatalf("upParams: %v", err)
	}
	if params.Name != nil {
		t.Errorf("name = %v, want nil when the group comes from the cwd", params.Name)
	}
	if params.ConfigPath == nil || filepath.Base(*params.ConfigPath) != groups.ConfigName {
		t.Fatalf("config_path = %v", params.ConfigPath)
	}
}

func TestUpParamsPassesOutsideHomeOptIn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, groups.ConfigName),
		[]byte("name: demo\nservices:\n  - name: api\n    cmd: uv run api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	upOnly = nil
	upAllowOutsideHome = true
	t.Cleanup(func() { upAllowOutsideHome = false })

	params, _, err := upParams(nil)
	if err != nil {
		t.Fatalf("upParams: %v", err)
	}
	if !params.AllowOutsideHome {
		t.Fatal("allow_outside_home = false, want the explicit CLI opt-in")
	}
}

// TestUpParamsPassesTheGroupName straight through, so the daemon resolves it
// against every config it knows and not only the one under the cwd.
func TestUpParamsPassesTheGroupName(t *testing.T) {
	upOnly = []string{"api", "db"}
	t.Cleanup(func() { upOnly = nil })

	params, _, err := upParams([]string{"  storefront "})
	if err != nil {
		t.Fatalf("upParams: %v", err)
	}
	if params.Name == nil || *params.Name != "storefront" {
		t.Fatalf("name = %v, want storefront", params.Name)
	}
	if params.ConfigPath != nil {
		t.Errorf("config_path = %v, want nil when a group is named", params.ConfigPath)
	}
	if len(params.Only) != 2 || params.Only[0] != "api" {
		t.Errorf("only = %v", params.Only)
	}
}

// TestUpParamsWithoutAConfig says what to do next rather than just failing.
func TestUpParamsWithoutAConfig(t *testing.T) {
	chdir(t, t.TempDir())
	upOnly = nil

	_, _, err := upParams(nil)
	if err == nil {
		t.Fatal("expected an error with no .oberth.yaml anywhere")
	}
	if !strings.Contains(err.Error(), "oberth init") {
		t.Errorf("error should say how to make one: %v", err)
	}
}

// TestUpParamsReportsABrokenConfig instead of pretending there is none: the
// two problems have completely different fixes.
func TestUpParamsReportsABrokenConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, groups.ConfigName),
		[]byte("name: two words\nservices:\n  - name: api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	upOnly = nil

	_, _, err := upParams(nil)
	if err == nil {
		t.Fatal("expected an error for an invalid config")
	}
	if !strings.Contains(err.Error(), "cannot be used") || !strings.Contains(err.Error(), "whitespace") {
		t.Errorf("error = %v, want it to name the validation problem", err)
	}
}

// Readiness is a desired-state check, not just a receipt for newly spawned runs.
// A pre-existing service that is not ready must fail without becoming our cleanup target.
func TestWaitForStartReadyChecksSkippedWithoutAuthorizingCleanup(t *testing.T) {
	observableProbe(t)
	t.Setenv("BERTH_HOME", t.TempDir())
	old := noDaemonFlag
	noDaemonFlag = true
	t.Cleanup(func() { noDaemonFlag = old })
	dir := t.TempDir()
	path := filepath.Join(dir, groups.ConfigName)
	if err := os.WriteFile(path, []byte("name: demo\nservices:\n  - name: api\n    cmd: echo api\n    port: 19001\n"), 0600); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	chunks := []rpc.GroupsStartChunk{{Service: "api", State: "skipped", Skipped: true}}
	summary := rpc.GroupsStartEnd{Skipped: []string{"api"}}
	cleanup, err := waitForStartReady(context.Background(), rpc.GroupsStartParams{ConfigPath: &path}, nil, chunks, &summary, time.Millisecond, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleanup) != 0 {
		t.Fatalf("pre-existing run authorized for cleanup: %v", cleanup)
	}
	if chunks[0].State != "failed" || chunks[0].Reason != "ready_timeout" || len(summary.Errors) != 1 {
		t.Fatalf("unready reused service silently accepted: chunks=%+v summary=%+v", chunks, summary)
	}
	if !chunks[0].Skipped {
		t.Fatal("lost the original no-spawn fact")
	}
}

func TestWaitForStartReadyMixedTimeoutCleansOnlyNewStarts(t *testing.T) {
	observableProbe(t)
	t.Setenv("BERTH_HOME", t.TempDir())
	old := noDaemonFlag
	noDaemonFlag = true
	t.Cleanup(func() { noDaemonFlag = old })
	dir := t.TempDir()
	path := filepath.Join(dir, groups.ConfigName)
	data := "name: demo\nservices:\n  - name: old\n    cmd: echo old\n    port: 19001\n  - name: new\n    cmd: echo new\n    port: 19002\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	chunks := []rpc.GroupsStartChunk{{Service: "old", State: "skipped", Skipped: true}, {Service: "new", State: "started"}}
	summary := rpc.GroupsStartEnd{Skipped: []string{"old"}, Started: []string{"new"}}
	cleanup, err := waitForStartReady(context.Background(), rpc.GroupsStartParams{ConfigPath: &path}, nil, chunks, &summary, time.Millisecond, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleanup) != 1 || cleanup[0] != "new" {
		t.Fatalf("cleanup=%v, must exclude existing service", cleanup)
	}
	if len(summary.Errors) != 2 {
		t.Fatalf("both services must fail readiness: %+v", summary)
	}
}
