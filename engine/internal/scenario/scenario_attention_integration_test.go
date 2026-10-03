//go:build integration

package scenario

// These tests exercise the attention handoff at the boundary where a real
// daemon and CLI produce the facts. Jev itself is covered with an injectable
// HTTP server in internal/attention; this file deliberately never calls a
// remote model. The only human step in the product remains the fixed option
// mapping documented by the skill and exercised by scripts/verify-attention-
// handoff.py.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type attentionEventDoc struct {
	Kind    string `json:"kind"`
	Service string `json:"service,omitempty"`
	Machine string `json:"machine,omitempty"`
	Port    int    `json:"port,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type attentionDoc struct {
	Schema        string              `json:"schema"`
	EventID       string              `json:"event_id"`
	StateRevision string              `json:"state_revision"`
	WorktreeRoot  string              `json:"worktree_root"`
	FreshUntil    string              `json:"fresh_until"`
	Event         attentionEventDoc   `json:"event"`
	Events        []attentionEventDoc `json:"events"`
	Options       []struct {
		ID string `json:"id"`
	} `json:"options"`
}

func readAttentionDoc(t *testing.T, status statusDoc) attentionDoc {
	t.Helper()
	if status.AttentionPath == "" {
		t.Fatal("status has no attention_path")
	}
	data, err := os.ReadFile(status.AttentionPath)
	if err != nil {
		t.Fatalf("read attention artifact: %v", err)
	}
	var artifact attentionDoc
	if err := json.Unmarshal(data, &artifact); err != nil {
		t.Fatalf("decode attention artifact: %v\n%s", err, data)
	}
	if artifact.Schema != "oberth.attention/v1" {
		t.Fatalf("attention schema = %q", artifact.Schema)
	}
	if artifact.EventID == "" || artifact.StateRevision == "" || artifact.FreshUntil == "" {
		t.Fatalf("attention identity/freshness missing: %+v", artifact)
	}
	if status.AttentionRevision != artifact.StateRevision {
		t.Fatalf("status attention_revision = %q, artifact state_revision = %q", status.AttentionRevision, artifact.StateRevision)
	}
	return artifact
}

func attentionServices(a attentionDoc) map[string]string {
	got := make(map[string]string, len(a.Events))
	for _, event := range a.Events {
		if event.Service != "" {
			got[event.Service] = event.Kind + ":" + event.Reason
		}
	}
	return got
}

// TestAttentionFailureMatrix verifies that three simultaneous old facts are
// handed to the agent together, and that inspecting logs does not mutate or
// clear the request. A second failed run gets a new revision/identity, while
// repeated reads of the same run keep both stable.
func TestAttentionFailureMatrix(t *testing.T) {
	e := newEnv(t)
	e.serve()
	b, err := FailureMatrix(filepath.Join(e.home, "attention-failures"))
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
	if err := e.runJSON(b.Root, &up, "up", "--only", "missing,exited,occupied", "--json"); err == nil {
		t.Fatal("failure matrix unexpectedly started")
	}
	firstStatus := e.waitStatus(b.Root, func(status statusDoc) bool {
		// Start failures are synchronous, child exits are not. The presence
		// of the first attention artifact does not mean every run has exited.
		// Wait for the underlying terminal facts, then independently verify
		// ALL event contents below. Keep the existing bounded wait budget.
		if status.AttentionPath == "" || status.AttentionRevision == "" {
			return false
		}
		for _, name := range []string{"missing", "exited", "occupied"} {
			service, ok := status.service(name)
			if !ok || service.Running || service.LastExit == nil {
				return false
			}
		}
		return true
	})
	first := readAttentionDoc(t, firstStatus)
	want := map[string]string{
		"missing":  "service_failed:start_failed",
		"exited":   "service_failed:crashed",
		"occupied": "port_occupied:port_occupied",
	}
	if got := attentionServices(first); len(got) != len(want) {
		t.Fatalf("attention events = %+v, want all simultaneous failures %+v", got, want)
	} else {
		for service, expected := range want {
			if got[service] != expected {
				t.Errorf("attention event %s = %q, want %q", service, got[service], expected)
			}
		}
	}
	if first.Event.Service == "" || first.Event.Service != first.Events[0].Service {
		t.Fatalf("compatibility event = %+v, want first events[0]", first.Event)
	}
	if len(first.Options) == 0 {
		t.Fatal("attention has no fixed options")
	}

	repeatStatus := e.waitStatus(b.Root, func(status statusDoc) bool {
		return status.AttentionPath == firstStatus.AttentionPath && status.AttentionRevision == first.StateRevision
	})
	repeat := readAttentionDoc(t, repeatStatus)
	if repeat.EventID != first.EventID || repeat.StateRevision != first.StateRevision {
		t.Fatalf("same failed run changed identity: first=%+v repeat=%+v", first, repeat)
	}
	var logs struct {
		Lines []string `json:"lines"`
	}
	if err := e.runJSON(b.Root, &logs, "logs", "exited", "--once", "--json"); err != nil {
		t.Fatalf("inspect logs: %v", err)
	}
	afterLogs := e.waitStatus(b.Root, func(status statusDoc) bool { return status.AttentionPath != "" })
	afterLogArtifact := readAttentionDoc(t, afterLogs)
	if afterLogArtifact.EventID != first.EventID {
		t.Fatalf("read-only log inspection changed event identity: %q -> %q", first.EventID, afterLogArtifact.EventID)
	}

	// The same command failing a second time is a new run, not a duplicate
	// handoff. This prevents an agent from treating a fresh crash as already
	// acknowledged merely because the service name is unchanged.
	if err := e.runJSON(b.Root, &up, "up", "--only", "exited", "--json"); err == nil {
		t.Fatal("second exited run unexpectedly succeeded")
	}
	secondStatus := e.waitStatus(b.Root, func(status statusDoc) bool {
		return status.AttentionPath != "" && status.AttentionRevision != first.StateRevision
	})
	second := readAttentionDoc(t, secondStatus)
	if second.EventID == first.EventID || second.StateRevision == first.StateRevision {
		t.Fatalf("repeated crash was deduplicated as the old event: first=%+v second=%+v", first, second)
	}
}

// TestAttentionNormalAndUndeclaredBoundaries records both sides of the
// boundary: ordinary runtime activity does not interrupt, while a listener
// inside this worktree with no manifest declaration is a deterministic anomaly
// that does create an attention request. A normal down then clears it.
func TestAttentionNormalAndUndeclaredBoundaries(t *testing.T) {
	e := newEnv(t)
	e.serve()
	b, err := APIServerAndWorker(filepath.Join(e.home, "attention-normal"))
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
	if err := e.runJSON(b.Root, &up, "up", "--json"); err != nil || len(up.Errors) != 0 {
		t.Fatalf("up = %+v, err=%v", up, err)
	}
	status := e.waitStatus(b.Root, func(status statusDoc) bool {
		return status.serviceRunning("api", b.Port, true) && status.AttentionPath == ""
	})
	_ = status
	var logs struct {
		Lines []string `json:"lines"`
	}
	if err := e.runJSON(b.Root, &logs, "logs", "api", "--once", "--json"); err != nil {
		t.Fatalf("ordinary log read: %v", err)
	}
	if got := e.waitStatus(b.Root, func(status statusDoc) bool { return status.AttentionPath == "" }); got.AttentionPath != "" {
		t.Fatal("ordinary log read created attention")
	}

	extraPort := freePort()
	undeclaredProcess := startUndeclaredListener(t, e, b.Root, extraPort)
	undeclared := e.waitStatus(b.Root, func(status statusDoc) bool {
		return portsContain(status, extraPort) && status.AttentionPath != ""
	})
	artifact := readAttentionDoc(t, undeclared)
	foundUndeclared := false
	for _, event := range artifact.Events {
		if event.Kind == "undeclared_listener" && event.Port == extraPort {
			foundUndeclared = true
			break
		}
	}
	if !foundUndeclared {
		t.Fatalf("attention did not identify undeclared listener %d: %+v", extraPort, artifact.Events)
	}
	if err := undeclaredProcess.Process.Kill(); err != nil {
		t.Fatalf("stop undeclared listener: %v", err)
	}
	if err := undeclaredProcess.Wait(); err != nil {
		// The fixture is intentionally killed; its non-zero wait status is expected.
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("wait for undeclared listener: %v", err)
		}
	}
	var down struct {
		OK bool `json:"ok"`
	}
	if err := e.runJSON(b.Root, &down, "down", "--json"); err != nil || !down.OK {
		t.Fatalf("normal down = %+v, err=%v", down, err)
	}
	if got := e.waitStatus(b.Root, func(status statusDoc) bool {
		api, ok := status.service("api")
		return ok && !api.Running && status.AttentionPath == ""
	}); got.AttentionPath != "" {
		t.Fatal("normal stop left an attention request")
	}
}

// TestAttentionManifestConflictIsNotActionable makes a malformed duplicate
// service declaration after a clean run. Status must fail at the manifest
// boundary; it must not turn a parser/configuration conflict into a guessed
// service action or a new attention artifact.
func TestAttentionManifestConflictIsNotActionable(t *testing.T) {
	e := newEnv(t)
	e.serve()
	b, err := APIServerAndWorker(filepath.Join(e.home, "attention-conflict"))
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
	if err := e.runJSON(b.Root, &up, "up", "--json"); err != nil || len(up.Errors) != 0 {
		t.Fatalf("up = %+v, err=%v", up, err)
	}
	clean := e.waitStatus(b.Root, func(status statusDoc) bool {
		return status.AttentionPath == ""
	})
	_ = clean
	manifest, err := os.ReadFile(b.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	conflict := strings.Replace(string(manifest), "  - name: api\n", "  - name: api\n  - name: api\n", 1)
	if err := os.WriteFile(b.Manifest, []byte(conflict), 0o644); err != nil {
		t.Fatal(err)
	}
	// Stop the daemon so status must parse the changed manifest rather than
	// serving the old indexed group snapshot.
	if _, err := e.run("", "daemon", "stop", "--json"); err != nil {
		t.Fatalf("daemon stop: %v", err)
	}
	stdout, stderr, err := e.runCapture(b.Root, "status", "--json", "--no-mark")
	if err == nil || len(stdout) != 0 || !strings.Contains(string(stderr), "duplicate") {
		t.Fatalf("conflicting manifest status: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if _, err := os.Stat(clean.AttentionPath); clean.AttentionPath != "" && err == nil {
		t.Fatalf("manifest conflict left an actionable artifact at %s", clean.AttentionPath)
	}
}
