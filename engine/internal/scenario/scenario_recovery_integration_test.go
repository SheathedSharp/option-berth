//go:build integration

package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/client"
)

// TestDaemonStopRestartKeepsServiceEvidence proves that stopping the
// collector does not silently stop a service it started. Both the direct scan
// while the daemon is down and the resumed daemon must describe the same run;
// logs must remain available throughout.
func TestDaemonStopRestartKeepsServiceEvidence(t *testing.T) {
	e := newEnv(t)
	e.serve()
	b, err := RecoveryService(filepath.Join(e.home, "recovery"))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() {
		e.cleanupBuilt(b)
		_, _ = e.run("", "daemon", "stop", "--json")
	})

	var up struct {
		Errors []string `json:"errors"`
	}
	if err := e.runJSON(b.Root, &up, "up", "--json"); err != nil {
		t.Fatalf("up: %v", err)
	}
	if len(up.Errors) != 0 {
		t.Fatalf("up errors = %v", up.Errors)
	}
	e.waitStatus(b.Root, func(st statusDoc) bool { return st.serviceRunning("api", b.Port, true) })

	// Establish a baseline, then read it without moving the mark. This pins
	// the two status modes together: --no-mark must compare against the same
	// anchor an ordinary status read established.
	var marked statusDoc
	if err := e.runJSON(b.Root, &marked, "status", "--json"); err != nil {
		t.Fatalf("status baseline: %v", err)
	}
	if marked.Changed != nil {
		t.Fatalf("first status baseline has changed data: %+v", marked.Changed)
	}
	var peek statusDoc
	if err := e.runJSON(b.Root, &peek, "status", "--json", "--no-mark"); err != nil {
		t.Fatalf("status --no-mark baseline: %v", err)
	}
	if peek.Changed == nil || !peek.Changed.Empty || peek.Changed.At == "" {
		t.Fatalf("status --no-mark baseline = %+v, want an empty comparison with an anchor", peek.Changed)
	}

	// Several independent CLI processes may ask the daemon for the same state
	// at once. Every response must remain valid and retain the service's
	// attribution; a transient SQLite/daemon serialization error is a failure.
	const callers = 8
	var wg sync.WaitGroup
	errCh := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var got statusDoc
			if err := e.runJSON(b.Root, &got, "status", "--json", "--no-mark"); err != nil {
				errCh <- err
				return
			}
			if !got.serviceRunning("api", b.Port, true) {
				errCh <- fmt.Errorf("concurrent status lost api attribution: %+v", got.Worktree.Services)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	// Keep the daemon alive across several scanner ticks. The scan counter must
	// advance while the service remains attributed and its log remains readable.
	var previousScans = -1
	advances := 0
	deadline := time.Now().Add(25 * time.Second)
	for advances < 3 && time.Now().Before(deadline) {
		var daemonStatus struct {
			Scans int `json:"scans"`
		}
		if err := e.runJSON("", &daemonStatus, "daemon", "status", "--json"); err != nil {
			t.Fatalf("daemon status during continuous run: %v", err)
		}
		if previousScans >= 0 && daemonStatus.Scans > previousScans {
			advances++
		}
		previousScans = daemonStatus.Scans
		e.waitStatus(b.Root, func(st statusDoc) bool { return st.serviceRunning("api", b.Port, true) })
		time.Sleep(250 * time.Millisecond)
	}
	if advances < 3 {
		t.Fatalf("scanner stopped advancing after %d ticks (last scan %d)", advances, previousScans)
	}

	var logs struct {
		Lines   []string `json:"lines"`
		Service string   `json:"service"`
		RunID   string   `json:"run_id"`
		Status  string   `json:"status"`
		PID     int      `json:"pid"`
	}
	if err := e.runJSON(b.Root, &logs, "logs", "api", "--once", "--json"); err != nil {
		t.Fatalf("logs before stop: %v", err)
	}
	if !strings.Contains(strings.Join(logs.Lines, "\n"), "recovery service ready") {
		t.Fatalf("logs before stop = %q", logs.Lines)
	}
	if logs.Service != "api" || logs.RunID == "" || logs.Status != "running" || logs.PID <= 0 {
		t.Fatalf("logs metadata before stop = %+v, want the running api run", logs)
	}

	if _, err := e.run("", "daemon", "stop", "--json"); err != nil {
		t.Fatalf("daemon stop: %v", err)
	}
	stdout, _, err := e.runCapture(b.Root, "status", "--json", "--no-mark")
	if err != nil {
		t.Fatalf("status with daemon stopped: %v", err)
	}
	var direct statusDoc
	if err := json.Unmarshal(stdout, &direct); err != nil {
		t.Fatalf("decode direct status: %v\n%s", err, stdout)
	}
	if !direct.serviceRunning("api", b.Port, true) {
		t.Fatalf("direct status after daemon stop = %+v, want api still running", direct.Worktree.Services)
	}

	if _, err := e.run("", "daemon", "restart", "--json"); err != nil {
		t.Fatalf("daemon restart: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.WaitForSocket(ctx, e.socket, 10*time.Second); err != nil {
		t.Fatalf("replacement daemon never came up: %v", err)
	}
	e.waitStatus(b.Root, func(st statusDoc) bool { return st.serviceRunning("api", b.Port, true) })

	if err := e.runJSON(b.Root, &logs, "logs", "api", "--once", "--json"); err != nil {
		t.Fatalf("logs after restart: %v", err)
	}
	if !strings.Contains(strings.Join(logs.Lines, "\n"), "recovery service ready") {
		t.Fatalf("logs after restart = %q", logs.Lines)
	}
	if logs.Service != "api" || logs.RunID == "" || logs.Status != "running" || logs.PID <= 0 {
		t.Fatalf("logs metadata after restart = %+v, want the running api run", logs)
	}
}

// TestSIGTERMEscalatesAndCleansServiceTree verifies the default stop path:
// SIGTERM is attempted first, an ignoring supervisor is escalated to SIGKILL,
// and the child's undeclared listener disappears with it.
func TestSIGTERMEscalatesAndCleansServiceTree(t *testing.T) {
	e := newEnv(t)
	e.serve()
	b, err := ForceTree(filepath.Join(e.home, "force-tree"))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() { e.cleanupBuilt(b) })

	var up struct {
		Errors []string `json:"errors"`
	}
	if err := e.runJSON(b.Root, &up, "up", "--json"); err != nil {
		t.Fatalf("up: %v", err)
	}
	if len(up.Errors) != 0 {
		t.Fatalf("up errors = %v", up.Errors)
	}
	if !waitPortOpen(b.Port, 10*time.Second) || !waitPortOpen(b.ExtraPort, 10*time.Second) {
		t.Fatalf("tree listeners did not both start: parent=%d child=%d", b.Port, b.ExtraPort)
	}

	var down struct {
		OK      bool `json:"ok"`
		Results []struct {
			Method string `json:"method"`
			OK     bool   `json:"ok"`
		} `json:"results"`
	}
	if err := e.runJSON(b.Root, &down, "down", "--json"); err != nil {
		t.Fatalf("down: %v", err)
	}
	if !down.OK {
		t.Fatalf("down = %+v, want ok", down)
	}
	if len(down.Results) == 0 {
		t.Fatal("down returned no service stop result")
	}
	seenKill := false
	for _, row := range down.Results {
		if row.Method == "sigkill" {
			seenKill = true
		}
	}
	if !seenKill {
		t.Fatalf("down results = %+v, want SIGKILL escalation", down.Results)
	}
	// Signal delivery can precede kernel socket teardown. Keep the same bounded
	// observation window, but poll for confirmed closure rather than fail on
	// the very first successful dial; unknown/timeouts are not closure.
	if !waitPortClosed(b.Port, 300*time.Millisecond) || !waitPortClosed(b.ExtraPort, 300*time.Millisecond) {
		t.Fatalf("listener survived tree cleanup: parent=%d child=%d", b.Port, b.ExtraPort)
	}
}
