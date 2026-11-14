//go:build integration

package runtimesvc_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// doctorEnvironmentChecks are the rows that must come back clean on a machine
// where option-berth itself is set up correctly: the runtime service, its socket, its
// database and the user's config. The integration checks — MCP clients, the
// skill, the hooks, a project's oberth.yaml, docker — describe the *user's*
// machine and are warnings on a fresh temp HOME by design, so nothing here
// asserts them.
var doctorEnvironmentChecks = []string{
	"config_parses", "config_dir_writable",
	"runtime_service_reachable", "runtime_service_version_matches", "runtime_service_protocol",
	"socket_permissions", "db_ok",
}

// checksByID keys a doctor report's rows by check id.
func checksByID(report rpc.RuntimeServiceDoctorResult) map[string]rpc.DoctorCheck {
	byID := make(map[string]rpc.DoctorCheck, len(report.Checks))
	for _, check := range report.Checks {
		byID[check.ID] = check
	}
	return byID
}

// doctorReport runs `oberth doctor --json` over the named checks and hands back
// the decoded report together with the raw output.
func doctorReport(t *testing.T, sandbox *env, only ...string) (rpc.RuntimeServiceDoctorResult, []byte) {
	t.Helper()
	out, err := sandbox.command("doctor", "--json", "--only", strings.Join(only, ",")).CombinedOutput()
	if err != nil {
		t.Fatalf("oberth doctor exited non-zero: %v\n%s", err, out)
	}
	var report rpc.RuntimeServiceDoctorResult
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("oberth doctor --json did not print JSON: %v\n%s", err, out)
	}
	return report, out
}

// TestDoctorAgainstARunningRuntimeService is the acceptance demo: `oberth doctor`
// against a runtime service this test started reports every environment check ok and
// exits 0.
func TestDoctorAgainstARunningRuntimeService(t *testing.T) {
	sandbox := newEnv(t)
	sandbox.serve()

	report, raw := doctorReport(t, sandbox, doctorEnvironmentChecks...)
	if !report.OK {
		t.Errorf("report is not ok:\n%s", raw)
	}
	assertHealthy(t, report, doctorEnvironmentChecks)
	if report.RuntimeServiceVersion == "" {
		t.Error("the report named no runtime service version")
	}
}

// TestDoctorExitsNonZeroWithNoRuntimeService covers the other half of the exit-code
// contract: a script can branch on it.
func TestDoctorExitsNonZeroWithNoRuntimeService(t *testing.T) {
	sandbox := newEnv(t)
	out, err := sandbox.command("doctor", "--only", "runtime_service_reachable").CombinedOutput()
	if err == nil {
		t.Fatalf("oberth doctor exited 0 with no runtime service running:\n%s", out)
	}
	if said := string(out); !strings.Contains(said, "no runtime service is listening") {
		t.Errorf("output does not say what is wrong:\n%s", said)
	}
}

// TestRuntimeServiceDoctorRPC is what the desktop app calls: the same rows, without
// shelling out, with the CLI-only ones marked as such.
func TestRuntimeServiceDoctorRPC(t *testing.T) {
	sandbox := newEnv(t)
	sandbox.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cl := sandbox.connect(ctx)

	only := append([]string{"cli_on_path"}, doctorEnvironmentChecks...)
	var report rpc.RuntimeServiceDoctorResult
	if err := cl.Call(ctx, "runtime.service.doctor", rpc.RuntimeServiceDoctorParams{Only: only}, &report); err != nil {
		t.Fatalf("runtime.service.doctor: %v", err)
	}
	if !report.OK {
		t.Errorf("runtime.service.doctor is not ok: %+v", report.Checks)
	}

	byID := checksByID(report)
	cli, ok := byID["cli_on_path"]
	if !ok {
		t.Fatal("runtime.service.doctor dropped the cli_on_path row instead of skipping it")
	}
	if cli.Status != "skip" || !strings.Contains(cli.Detail, "CLI-only") {
		t.Errorf("cli_on_path = %+v, want a skip whose detail calls it CLI-only", cli)
	}
	// The runtime service reports on itself rather than dialling its own socket.
	if reachable := byID["runtime_service_reachable"]; reachable.Status != "ok" ||
		!strings.Contains(reachable.Detail, "from the runtime service itself") {
		t.Errorf("runtime_service_reachable = %+v", reachable)
	}
	assertHealthy(t, report, doctorEnvironmentChecks)

	// The same runtime service serves the refusals below: a second one costs a whole
	// process, and an unknown id must be refused rather than silently answered
	// with an empty report.
	t.Run("an unknown check is refused", func(t *testing.T) {
		var out rpc.RuntimeServiceDoctorResult
		err := cl.Call(ctx, "runtime.service.doctor", rpc.RuntimeServiceDoctorParams{Only: []string{"nope"}}, &out)
		if err == nil {
			t.Fatal("an unknown check id was accepted")
		}
		if !strings.Contains(err.Error(), "nope") {
			t.Errorf("error = %v, want it to name the unknown check", err)
		}
	})

	// A relative --project would mean the runtime service's own working directory, which
	// is never what the caller meant.
	t.Run("a relative project is refused", func(t *testing.T) {
		var out rpc.RuntimeServiceDoctorResult
		err := cl.Call(ctx, "runtime.service.doctor", rpc.RuntimeServiceDoctorParams{Project: "."}, &out)
		if err == nil {
			t.Fatal("a relative project path was accepted")
		}
	})
}

// assertHealthy fails the test for every id that did not come back ok. A skip
// passes only where the platform genuinely has nothing to look at — Windows has
// no socket file — and never silently: the row still has to say so.
func assertHealthy(t *testing.T, report rpc.RuntimeServiceDoctorResult, ids []string) {
	t.Helper()
	byID := checksByID(report)
	for _, id := range ids {
		check, ok := byID[id]
		if !ok {
			t.Errorf("%s is missing from the report", id)
			continue
		}
		switch {
		case check.Status == "ok":
			// The row is what the environment check is supposed to say.
		case check.Status == "skip" && (id == "socket_permissions" || id == "runtime_service_version_matches"):
			// No socket file on Windows; no CLI to compare over RPC.
		default:
			t.Errorf("%s = %s (%s / %s), want ok", id, check.Status, check.Summary, check.Detail)
		}
	}
}
