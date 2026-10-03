//go:build integration

package scenario

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runCapture keeps stdout and stderr separate. This is important for the CLI
// contract: a failed --json command has its structured answer on stdout (or,
// for an error outside a stream, stderr) and must never be judged by merged
// human text.
func (e *env) runCapture(dir string, args ...string) (stdout, stderr []byte, err error) {
	e.t.Helper()
	cmd := e.command(dir, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

func matrixService(st statusDoc, name string) (running bool, exit *struct {
	Code   int    `json:"code"`
	Reason string `json:"reason"`
}, found bool) {
	for _, svc := range st.Worktree.Services {
		if svc.Name == name {
			return svc.Running, svc.LastExit, true
		}
	}
	return false, nil, false
}

// TestFailureRecoveryMatrix exercises the user-facing failure evidence in one
// isolated project. Each failure stays attached to its service: up reports a
// per-service chunk, status preserves the distinction between a service that
// was never started and one that crashed, and logs remains useful after exit.
func TestFailureRecoveryMatrix(t *testing.T) {
	e := newEnv(t)
	e.serve()
	b, err := FailureMatrix(filepath.Join(e.home, "failure-matrix"))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() {
		e.cleanupBuilt(b)
		_, _ = e.run("", "daemon", "stop", "--json")
	})

	// A terminal-evidence test must also cover a real bind failure arriving
	// after the launcher's short observation window, not just on an idle host.
	occupiedPath := filepath.Join(b.Root, "occupied.py")
	occupiedCode, err := os.ReadFile(occupiedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(occupiedPath, append([]byte("import time; time.sleep(0.2)\n"), occupiedCode...), 0o600); err != nil {
		t.Fatal(err)
	}

	// Before up, every service is declared but untouched. This is the stable
	// "not started" state: no live run and no last-exit record.
	initial := e.waitStatus(b.Root, func(st statusDoc) bool {
		for _, name := range []string{"missing", "exited", "occupied", "idle"} {
			running, exit, found := matrixService(st, name)
			if !found || running || exit != nil {
				return false
			}
		}
		return true
	})
	if initial.Worktree.Status != "stopped" {
		t.Fatalf("initial status = %q, want stopped", initial.Worktree.Status)
	}

	// This matrix asserts terminal startup evidence, not that a Python
	// interpreter always reaches bind() within the launcher's 100ms window.
	// The occupied fixture deliberately fails later; --wait must still report
	// all three service failures before returning.
	stdout, stderr, runErr := e.runCapture(b.Root, "up", "--wait", "--only", "missing,exited,occupied", "--json")
	if runErr == nil {
		t.Fatal("up succeeded even though every selected service should fail")
	}
	if len(stderr) != 0 {
		t.Fatalf("up wrote a daemon-level error to stderr: %s", stderr)
	}
	var up struct {
		Services []struct {
			Service string `json:"service"`
			Error   string `json:"error"`
		} `json:"services"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(stdout, &up); err != nil {
		t.Fatalf("decode up --json: %v\n%s", err, stdout)
	}
	if len(up.Errors) != 3 || len(up.Services) != 3 {
		t.Fatalf("up = %+v, want three service failures", up)
	}
	for _, svc := range up.Services {
		if svc.Error == "" {
			t.Errorf("service %q has no service-level error", svc.Service)
		}
	}

	// Every selected service now leaves service-level evidence, including the
	// missing executable that failed before a process could be registered.
	st := e.waitStatus(b.Root, func(st statusDoc) bool {
		_, missing, ok0 := matrixService(st, "missing")
		_, exited, ok1 := matrixService(st, "exited")
		_, occupied, ok2 := matrixService(st, "occupied")
		return ok0 && ok1 && ok2 && missing != nil && exited != nil && occupied != nil
	})
	missingRunning, missingExit, _ := matrixService(st, "missing")
	if missingRunning || missingExit == nil || missingExit.Code != 1 || missingExit.Reason != "start_failed" {
		t.Errorf("missing command status = running=%v exit=%v, want start_failed evidence", missingRunning, missingExit)
	}
	var missingEvidence bool
	for _, run := range st.Exits {
		if run.Name == "missing" && strings.Contains(strings.Join(run.LastLines, "\n"), "not on PATH") {
			missingEvidence = true
			break
		}
	}
	if !missingEvidence {
		t.Errorf("status exits has no missing-command detail: %+v", st.Exits)
	}
	_, exited, _ := matrixService(st, "exited")
	if exited == nil || exited.Code != 7 || exited.Reason != "crashed" {
		t.Fatalf("exited status = %+v, want crashed exit 7", exited)
	}
	_, occupied, _ := matrixService(st, "occupied")
	if occupied == nil || occupied.Reason != "port_occupied" {
		t.Fatalf("occupied status = %+v, want port_occupied while the other process owns its port", occupied)
	}
	if !waitPortOpen(b.Port, 2*time.Second) {
		t.Fatal("the competing listener disappeared before status could report the port clash")
	}

	// A finished service's log remains readable, including the last line that
	// explains why it exited.
	var logs struct {
		Lines []string `json:"lines"`
	}
	if err := e.runJSON(b.Root, &logs, "logs", "exited", "--once", "--json"); err != nil {
		t.Fatalf("logs exited: %v", err)
	}
	if !strings.Contains(strings.Join(logs.Lines, "\n"), "exited with code 7") {
		t.Fatalf("logs = %q, want the exit marker", logs.Lines)
	}

	// A missing log is a service-level not_found answer, not a daemon failure.
	var logPath string
	for _, svc := range st.Worktree.Services {
		if svc.Name == "exited" && svc.LogPath != nil {
			logPath = *svc.LogPath
		}
	}
	if logPath == "" {
		t.Fatal("status did not publish exited service log_path")
	}
	if err := os.Remove(logPath); err != nil {
		t.Fatalf("remove log fixture: %v", err)
	}
	_, logErr, err := e.runCapture(b.Root, "logs", "exited", "--once", "--json")
	if err == nil || !strings.Contains(string(logErr), `"code": "not_found"`) {
		t.Fatalf("logs missing file = err %v, stderr %q; want not_found", err, logErr)
	}

	// The daemon may be down while a person is diagnosing the failure. Direct
	// status must still read the durable exit evidence from the local database.
	if _, err := e.run("", "daemon", "stop", "--json"); err != nil {
		t.Fatalf("daemon stop: %v", err)
	}
	directStdout, _, err := e.runCapture(b.Root, "status", "--json", "--no-mark")
	if err != nil {
		t.Fatalf("direct status after daemon stop: %v", err)
	}
	var direct statusDoc
	if err := json.Unmarshal(directStdout, &direct); err != nil {
		t.Fatalf("decode direct status: %v\n%s", err, directStdout)
	}
	seenExits := map[string]bool{}
	for _, run := range direct.Exits {
		seenExits[run.Name] = true
	}
	for _, name := range []string{"missing", "exited", "occupied"} {
		if !seenExits[name] {
			t.Errorf("direct status exits = %+v, want durable %s evidence", direct.Exits, name)
		}
		service, ok := direct.service(name)
		if !ok || service.LastExit == nil {
			t.Errorf("direct status service %q = %+v, want last_exit", name, service)
		}
	}
}
