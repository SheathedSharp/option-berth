package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/spf13/cobra"
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

// chdir moves into dir for the duration of a test and returns there after it.
func chdir(t *testing.T, dir string) {
	t.Helper()
	origin, err := os.Getwd()
	if err != nil {
		t.Fatalf("reading the working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("changing to %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(origin) })
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

func TestServiceReadyWaitsForDeclaredPortAfterCommandHealth(t *testing.T) {
	health := "command:docker compose exec -T db pg_isready"
	port := 15432
	group := state.Group{Services: []state.Service{{
		Name: "db", Running: true, PortAuto: true, Health: &health,
		HealthStatus: &state.Health{Status: state.HealthOK},
	}}}
	if ready, reason := serviceReady(group, nil, "db"); ready || reason != "waiting_for_listener" {
		t.Fatalf("serviceReady before the host port = %v, %q, want listener wait", ready, reason)
	}
	group.Services[0].PortActual = &port
	if ready, reason := serviceReady(group, nil, "db"); !ready || reason != "" {
		t.Fatalf("serviceReady after the host port = %v, %q, want ready", ready, reason)
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
	} else if len(timedOut) != 1 || timedOut[0].service != "api" {
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
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, groups.ConfigName),
		[]byte("name: catalog\nservices:\n  - name: web\n    cmd: uv run web\n"), 0o644); err != nil {
		t.Fatalf("writing the manifest: %v", err)
	}
	chdir(t, root)

	upOnly = nil
	upAllowOutsideHome = false
	resolved, _, err := upParams(nil)
	if err != nil {
		t.Fatalf("upParams: %v", err)
	}
	if resolved.Name != nil {
		t.Errorf("name = %v, want nil when the group comes from the cwd", resolved.Name)
	}
	if resolved.ConfigPath == nil || filepath.Base(*resolved.ConfigPath) != groups.ConfigName {
		t.Fatalf("config_path = %v", resolved.ConfigPath)
	}
}

func TestUpParamsPassesOutsideHomeOptIn(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, groups.ConfigName),
		[]byte("name: catalog\nservices:\n  - name: web\n    cmd: uv run web\n"), 0o644); err != nil {
		t.Fatalf("writing the manifest: %v", err)
	}
	chdir(t, root)
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
	upOnly = []string{"web", "db"}
	t.Cleanup(func() { upOnly = nil })

	params, _, err := upParams([]string{"  shopfront "})
	if err != nil {
		t.Fatalf("upParams: %v", err)
	}
	if params.Name == nil || *params.Name != "shopfront" {
		t.Fatalf("name = %v, want shopfront", params.Name)
	}
	if params.ConfigPath != nil {
		t.Errorf("config_path = %v, want nil when a group is named", params.ConfigPath)
	}
	if len(params.Only) != 2 || params.Only[0] != "web" {
		t.Errorf("only = %v", params.Only)
	}
}

// TestUpParamsWithoutAConfig says what to do next rather than only failing.
func TestUpParamsWithoutAConfig(t *testing.T) {
	chdir(t, t.TempDir())
	upOnly = []string(nil)

	_, _, runErr := upParams(nil)
	if runErr == nil {
		t.Fatal("expected an error with no .oberth.yaml anywhere")
	}
	if !strings.Contains(runErr.Error(), "oberth init") {
		t.Errorf("error should say how to make one: %v", runErr)
	}
}

// TestUpParamsReportsABrokenConfig instead of pretending there is none: the
// two problems have completely different fixes.
func TestUpParamsReportsABrokenConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, groups.ConfigName),
		[]byte("name: two words\nservices:\n  - name: web\n"), 0o644); err != nil {
		t.Fatalf("writing the manifest: %v", err)
	}
	chdir(t, root)
	upOnly = []string(nil)

	_, _, runErr := upParams(nil)
	if runErr == nil {
		t.Fatal("expected an error for an invalid config")
	}
	if !strings.Contains(runErr.Error(), "cannot be used") || !strings.Contains(runErr.Error(), "whitespace") {
		t.Errorf("error = %v, want it to name the validation problem", runErr)
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
	chunks := []rpc.GroupsStartChunk{{Service: "old", State: "skipped", Skipped: true}, {Service: "new", State: "started", RunID: "run-new"}}
	summary := rpc.GroupsStartEnd{Skipped: []string{"old"}, Started: []string{"new"}}
	cleanup, err := waitForStartReady(context.Background(), rpc.GroupsStartParams{ConfigPath: &path}, nil, chunks, &summary, time.Millisecond, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleanup) != 1 || cleanup[0].service != "new" || cleanup[0].runID != "run-new" {
		t.Fatalf("cleanup=%v, must be the run this invocation started and only it", cleanup)
	}
	if len(summary.Errors) != 2 {
		t.Fatalf("both services must fail readiness: %+v", summary)
	}
}

// TestUpParamsSendsDependencyClosureByDefault: `up` asks for the closure;
// --no-deps turns it off for a daemon that cannot do it, or for a caller that
// wants exactly what it named.
func TestUpParamsSendsDependencyClosureByDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, groups.ConfigName),
		[]byte("name: demo\nservices:\n  - name: api\n    cmd: echo api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	upOnly = []string{"api"}
	upNoDeps = false
	t.Cleanup(func() { upOnly = nil; upNoDeps = false })

	params, _, err := upParams(nil)
	if err != nil {
		t.Fatalf("upParams: %v", err)
	}
	if !params.IncludeDependencies {
		t.Fatal("include_dependencies = false, want the closure by default")
	}

	upNoDeps = true
	params, _, err = upParams(nil)
	if err != nil {
		t.Fatalf("upParams --no-deps: %v", err)
	}
	if params.IncludeDependencies {
		t.Fatal("include_dependencies = true, want --no-deps to turn it off")
	}

	// Without --only the field cannot change the plan, and asking for it would
	// break a plain `up` against a daemon that predates the closure.
	upNoDeps = false
	upOnly = nil
	params, _, err = upParams(nil)
	if err != nil {
		t.Fatalf("upParams without --only: %v", err)
	}
	if params.IncludeDependencies {
		t.Fatal("include_dependencies = true without --only, want the field left off")
	}

	upOnly = []string{" ", ""}
	params, _, err = upParams(nil)
	if err != nil {
		t.Fatalf("upParams with a blank --only: %v", err)
	}
	if params.IncludeDependencies {
		t.Fatal("include_dependencies = true for blank --only names, want the field left off")
	}
}

// TestStartDependencyCapabilityGate refuses a daemon that would ignore the
// field, instead of letting the missing services fail after the dependency
// timeout. A request that does not need the field is unaffected.
func TestStartDependencyCapabilityGate(t *testing.T) {
	old := rpc.DaemonHelloResult{DaemonVersion: "v0.9.0", Capabilities: []string{"groups"}}
	if err := requireStartDependencySupportFor(old, false); err != nil {
		t.Fatalf("a request without include_dependencies must pass: %v", err)
	}
	err := requireStartDependencySupportFor(old, true)
	if err == nil {
		t.Fatal("expected an error from a daemon without the capability")
	}
	for _, want := range []string{"depends_on closure", "oberth daemon restart", "--no-deps", "v0.9.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}

	newer := rpc.DaemonHelloResult{
		DaemonVersion: "v0.10.0",
		Capabilities:  []string{"groups", rpc.CapabilityStartDependencies},
	}
	if err := requireStartDependencySupportFor(newer, true); err != nil {
		t.Fatalf("a daemon announcing the capability must pass: %v", err)
	}
}

// TestPrintStartChunkMarksIncludedDependencies: a service brought in by a named
// root says so, and a named service's row stays as it was.
func TestPrintStartChunkMarksIncludedDependencies(t *testing.T) {
	started := true
	out := captureStdout(t, func() {
		printStartChunk(rpc.GroupsStartChunk{
			Service: "api", State: "started", Ready: &started, PID: 1,
			LogPath: "/tmp/api.log", IncludedBy: []string{"web"},
		})
	})
	if !strings.Contains(out, "(dependency of web)") {
		t.Fatalf("started dependency output = %q, want the included note", out)
	}

	out = captureStdout(t, func() {
		printStartChunk(rpc.GroupsStartChunk{
			Service: "db", State: "skipped", Skipped: true, Reason: "already running",
			IncludedBy: []string{"web"},
		})
	})
	if !strings.Contains(out, "required by web") || !strings.Contains(out, "already running") {
		t.Fatalf("skipped dependency output = %q, want reason and included note", out)
	}

	out = captureStdout(t, func() {
		printStartChunk(rpc.GroupsStartChunk{
			Service: "web", State: "started", Ready: &started, PID: 1, LogPath: "/tmp/web.log",
		})
	})
	if strings.Contains(out, "dependency of") {
		t.Fatalf("named service output = %q, want no included note", out)
	}
}

// TestReadinessNextNamesTheConfirmationCall: without --wait the command exits
// 0 after starting services whose readiness it did not confirm, so it must
// publish the call that does. `started` plus `ready:false` is not a failure,
// and polling sockets outside oberth is exactly what this replaces.
func TestReadinessNextNamesTheConfirmationCall(t *testing.T) {
	confirmed, unconfirmed := true, false
	cases := []struct {
		name   string
		chunks []rpc.GroupsStartChunk
		wait   bool
		want   string
	}{
		{"waited", []rpc.GroupsStartChunk{{Service: "api", State: "started", Ready: &unconfirmed}}, true, ""},
		{"organized names", []rpc.GroupsStartChunk{
			{Service: "web", State: "started"},
			{Service: "api", State: "started"},
		}, false, "oberth up --only api,web --wait --json"},
		{"explicit false", []rpc.GroupsStartChunk{{Service: "api", State: "started", Ready: &unconfirmed}}, false,
			"oberth up --only api --wait --json"},
		{"confirmed chunk", []rpc.GroupsStartChunk{{Service: "api", State: "started", Ready: &confirmed}}, false, ""},
		{"skipped only", []rpc.GroupsStartChunk{{Service: "api", State: "skipped", Skipped: true}}, false, ""},
		{"failure is not a readiness wait", []rpc.GroupsStartChunk{{Service: "api", State: "failed", Error: "boom"}}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := readinessNext(tc.chunks, tc.wait); got != tc.want {
				t.Fatalf("next = %q, want %q", got, tc.want)
			}
		})
	}
}

// The JSON document carries the same next step; an empty one is absent, not an
// empty string.
func TestUpDocumentCarriesTheNextStep(t *testing.T) {
	raw, err := json.Marshal(upDocument{
		Services:       []rpc.GroupsStartChunk{{Service: "api", State: "started"}},
		Next:           "oberth up --only api --wait --json",
		GroupsStartEnd: rpc.GroupsStartEnd{Started: []string{"api"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"next":"oberth up --only api --wait --json"`) {
		t.Fatalf("json = %s, want the next step", raw)
	}

	raw, err = json.Marshal(upDocument{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"next"`) {
		t.Fatalf("json = %s, want no next key when there is no step", raw)
	}
}

// TestPrintStartSummaryListsIncludedDependencies prints the closure only when
// there is one.
func TestPrintStartSummaryListsIncludedDependencies(t *testing.T) {
	out := captureStdout(t, func() {
		printStartSummary(rpc.GroupsStartEnd{
			Started:  []string{"api", "db", "web"},
			Included: []string{"api", "db"},
		})
	})
	if !strings.Contains(out, "included dependencies: api, db") {
		t.Fatalf("summary = %q, want the included line", out)
	}

	out = captureStdout(t, func() {
		printStartSummary(rpc.GroupsStartEnd{Started: []string{"api"}})
	})
	if strings.Contains(out, "included dependencies") {
		t.Fatalf("summary = %q, want no included line when nothing was included", out)
	}
}

// TestUpWaitModeValidatesBeforeTheStart: a timeout with no wait, or a
// non-positive timeout, is a usage error raised before any project lookup or
// RPC, while leaving the flags alone keeps each service's own budget.
func TestUpWaitModeValidatesBeforeTheStart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changed bool
		wait    bool
		timeout time.Duration
		wantErr bool
		want    upWaitMode
	}{
		{"no flags", false, false, 0, false, upWaitMode{}},
		{"wait without a timeout", false, true, 0, false, upWaitMode{wait: true}},
		{"a ceiling", true, true, 2 * time.Second, false, upWaitMode{wait: true, timeout: 2 * time.Second, timeoutSet: true}},
		{"a zero ceiling", true, true, 0, true, upWaitMode{}},
		{"a ceiling without wait", true, false, time.Second, true, upWaitMode{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := upWaitModeFromFlags(tc.changed, tc.wait, tc.timeout)
			if tc.wantErr {
				var usage usageError
				if !errors.As(err, &usage) {
					t.Fatalf("err = %v, want a usage error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want none", err)
			}
			if got != tc.want {
				t.Errorf("mode = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestRestartParamsDontIncludeDependencies: restart keeps its old meaning of
// exactly the named services.
func TestRestartParamsDontIncludeDependencies(t *testing.T) {
	params, _, err := projectStartParams([]string{"demo"}, []string{"api"}, false)
	if err != nil {
		t.Fatalf("projectStartParams: %v", err)
	}
	if params.IncludeDependencies {
		t.Fatal("restart params must not ask for the closure")
	}
}

// withUpFlags installs one flag combination for the duration of a test.
func withUpFlags(t *testing.T, set func()) {
	t.Helper()
	oldOnly, oldJSON, oldOutside := upOnly, upJSON, upAllowOutsideHome
	oldWait, oldNoDeps, oldTimeout := upWait, upNoDeps, upWaitTimeout
	t.Cleanup(func() {
		upOnly, upJSON, upAllowOutsideHome = oldOnly, oldJSON, oldOutside
		upWait, upNoDeps, upWaitTimeout = oldWait, oldNoDeps, oldTimeout
	})
	upOnly, upJSON, upAllowOutsideHome = nil, false, false
	upWait, upNoDeps, upWaitTimeout = false, false, 0
	set()
}

// upTestCommand is the cobra command `up` runs under: the RunE reads the
// --wait-timeout flag's Changed bit, so the flag has to exist.
func upTestCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("wait", false, "")
	cmd.Flags().Duration("wait-timeout", 0, "")
	return cmd
}

// TestUpRejectsABadReadinessFlagBeforeDoingAnything: readiness is validated
// before a project is resolved or an RPC is sent, so a typo cannot start half a
// project and then fail on the flag.
func TestUpRejectsABadReadinessFlagBeforeDoingAnything(t *testing.T) {
	withUpFlags(t, func() { upWait, upWaitTimeout = false, time.Second })
	cmd := upTestCommand()
	if err := cmd.Flags().Set("wait-timeout", "1s"); err != nil {
		t.Fatal(err)
	}

	err := upRun(cmd, nil)
	var usage usageError
	if !errors.As(err, &usage) {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if !strings.Contains(err.Error(), "--wait-timeout requires --wait") {
		t.Fatalf("err = %v, want the flag combination named", err)
	}
}

// TestUpRejectsANonPositiveTimeout: an explicit ceiling has to be a positive
// budget; zero would mean "give up immediately".
func TestUpRejectsANonPositiveTimeout(t *testing.T) {
	withUpFlags(t, func() { upWait, upWaitTimeout = true, 0 })
	cmd := upTestCommand()
	if err := cmd.Flags().Set("wait-timeout", "0s"); err != nil {
		t.Fatal(err)
	}

	err := upRun(cmd, nil)
	var usage usageError
	if !errors.As(err, &usage) {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("err = %v, want the positivity rule named", err)
	}
}

// TestUpWithoutAManifestFails: outside a project there is nothing to start and
// the error says what to do, without dialling the daemon.
func TestUpWithoutAManifestFails(t *testing.T) {
	t.Chdir(t.TempDir())
	withUpFlags(t, func() {})
	noDaemonReachable(t)

	err := upRun(upTestCommand(), nil)
	if err == nil || !strings.Contains(err.Error(), "oberth.yaml") {
		t.Fatalf("err = %v, want the missing-manifest error", err)
	}
}

// TestUpWithoutADaemonFails: starting services is the daemon's work; with none
// reachable the command reports the connection failure rather than claiming the
// project came up.
func TestUpWithoutADaemonFails(t *testing.T) {
	demoProject(t)
	withUpFlags(t, func() {})
	resetRouting(t)
	noDaemonReachable(t)

	if err := upRun(upTestCommand(), nil); err == nil {
		t.Fatal("up without a daemon returned nil")
	}
}

// TestUpStartsAProjectService drives the whole command against a real daemon:
// the manifest is resolved here, groups.start runs there, and the summary
// reports the service the daemon actually started. The service is a short sleep,
// so it reclaims itself and leaves nothing behind.
func TestUpStartsAProjectService(t *testing.T) {
	dir := t.TempDir()
	const manifest = "name: demo\nservices:\n  - name: worker\n    cmd: sh -c \"sleep 1\"\n"
	if err := os.WriteFile(filepath.Join(dir, "oberth.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	// The checkout is a test temp directory, outside the user's home.
	withUpFlags(t, func() { upAllowOutsideHome = true })
	resetRouting(t)
	startDaemonWhereTheCommandLooks(t)

	var err error
	out := captureStdout(t, func() { err = upRun(upTestCommand(), nil) })
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if !strings.Contains(out, "worker") {
		t.Fatalf("stdout = %q, want the started service named", out)
	}
	if !strings.Contains(out, "1 started") {
		t.Fatalf("stdout = %q, want the summary of what the daemon started", out)
	}
}

// TestUpNamedProjectMustBeResolvable: a named project is the daemon's to find,
// so the failure is the daemon's own and is reported as such.
func TestUpNamedProjectMustBeResolvable(t *testing.T) {
	withUpFlags(t, func() {})
	resetRouting(t)
	startDaemonWhereTheCommandLooks(t)

	err := upRun(upTestCommand(), []string{"no-such-project"})
	if err == nil {
		t.Fatal("up of an unknown project returned nil")
	}
	if got := exitCodeFor(err); got != exitFail {
		t.Fatalf("exit code = %d, want %d", got, exitFail)
	}
}
