package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/state"
)

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
	if timedOut, err := waitForStartReady(context.Background(), params, nil, chunks, &summary, time.Millisecond); err != nil {
		t.Fatal(err)
	} else if len(timedOut) != 1 || timedOut[0] != "api" {
		t.Fatalf("timed out = %v, want api", timedOut)
	}
	if chunks[0].State != "failed" || chunks[0].Reason != "ready_timeout" ||
		chunks[0].Hint != "oberth logs api --once" || len(summary.Errors) != 1 {
		t.Fatalf("chunks = %+v, summary = %+v, want structured readiness timeout", chunks, summary)
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
