//go:build integration

package scenario

// These tests exercise the attention handoff at the boundary where a real
// runtime service and CLI produce the facts. model itself is covered with an injectable
// HTTP server in internal/attn; this file deliberately never calls a
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

type attnEventDoc struct {
	Kind    string `json:"kind"`
	Service string `json:"service,omitempty"`
	Machine string `json:"machine,omitempty"`
	Port    int    `json:"port,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type attnDoc struct {
	Schema        string         `json:"schema"`
	EventID       string         `json:"event_id"`
	StateRevision string         `json:"state_revision"`
	WorktreeRoot  string         `json:"worktree_root"`
	FreshUntil    string         `json:"fresh_until"`
	Event         attnEventDoc   `json:"event"`
	Events        []attnEventDoc `json:"events"`
	Options       []struct {
		ID string `json:"id"`
	} `json:"options"`
}

func readAttnDoc(t *testing.T, status statusDoc) attnDoc {
	t.Helper()
	if status.AttnPath == "" {
		t.Fatal("status has no attn_path")
	}
	data, err := os.ReadFile(status.AttnPath)
	if err != nil {
		t.Fatalf("read attention artifact: %v", err)
	}
	var artifact attnDoc
	if err := json.Unmarshal(data, &artifact); err != nil {
		t.Fatalf("decode attention artifact: %v\n%s", err, data)
	}
	if artifact.Schema != "oberth.attn/v1" {
		t.Fatalf("attention schema = %q", artifact.Schema)
	}
	if artifact.EventID == "" || artifact.StateRevision == "" || artifact.FreshUntil == "" {
		t.Fatalf("attention identity/freshness missing: %+v", artifact)
	}
	if status.AttnRevision != artifact.StateRevision {
		t.Fatalf("status attn_revision = %q, artifact state_revision = %q", status.AttnRevision, artifact.StateRevision)
	}
	return artifact
}

// enableAttentionIn is the person's consent, written where the runtime service's child
// processes read it. `status` publishes `attn_path` only once the
// enhancement is enabled, so every test here that expects an artifact has to
// state that the person turned it on — the same precondition a real install
// has after `oberth jev enable`. Without it these tests would assert against a
// feature that is deliberately off, not against the handoff they describe.
//
// The key is a placeholder: this file never calls a provider (an injectable
// HTTP server covers model itself), and the artifact is derived from runtime
// facts before any network step.
func enableAttentionIn(t *testing.T, e *env) {
	t.Helper()
	dir := filepath.Join(e.home, ".option-berth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating the model settings directory: %v", err)
	}
	body := `{"schema":"oberth.attn-settings/v1","enabled":true,"endpoint":"https://assess.example/decisions","model":"test/model","api_key":"integration-placeholder"}`
	if err := os.WriteFile(filepath.Join(dir, "attn.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing the model settings file: %v", err)
	}
}

func attnServices(a attnDoc) map[string]string {
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
	e.runRuntimeService()
	enableAttentionIn(t, e)
	b, err := FailureMatrix(filepath.Join(e.home, "attention-failures"))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() {
		e.cleanupBuilt(b)
		_, _ = e.run("", "runtime", "service", "stop", "--json")
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
		if status.AttnPath == "" || status.AttnRevision == "" {
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
	first := readAttnDoc(t, firstStatus)
	want := map[string]string{
		"missing":  "service_failed:start_failed",
		"exited":   "service_failed:crashed",
		"occupied": "port_occupied:port_occupied",
	}
	if got := attnServices(first); len(got) != len(want) {
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
		return status.AttnPath == firstStatus.AttnPath && status.AttnRevision == first.StateRevision
	})
	repeat := readAttnDoc(t, repeatStatus)
	if repeat.EventID != first.EventID || repeat.StateRevision != first.StateRevision {
		t.Fatalf("same failed run changed identity: first=%+v repeat=%+v", first, repeat)
	}
	var logs struct {
		Lines []string `json:"lines"`
	}
	if err := e.runJSON(b.Root, &logs, "logs", "exited", "--once", "--json"); err != nil {
		t.Fatalf("inspect logs: %v", err)
	}
	afterLogs := e.waitStatus(b.Root, func(status statusDoc) bool { return status.AttnPath != "" })
	afterLogArtifact := readAttnDoc(t, afterLogs)
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
		return status.AttnPath != "" && status.AttnRevision != first.StateRevision
	})
	second := readAttnDoc(t, secondStatus)
	if second.EventID == first.EventID || second.StateRevision == first.StateRevision {
		t.Fatalf("repeated crash was deduplicated as the old event: first=%+v second=%+v", first, second)
	}
}

// TestAttentionNormalAndUndeclaredBoundaries records both sides of the
// boundary: ordinary runtime activity does not interrupt, and a listener inside
// this worktree that no service declares is a fact `status` already reports —
// port and owner — not an abnormality worth a handoff. The agent did not start
// it and must not stop it, so turning it into an event only recreates the most
// expensive detour in the core loop. A normal down then leaves nothing behind.
func TestAttentionNormalAndUndeclaredBoundaries(t *testing.T) {
	e := newEnv(t)
	e.runRuntimeService()
	enableAttentionIn(t, e)
	b, err := APIServerAndWorker(filepath.Join(e.home, "attention-normal"))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() {
		e.cleanupBuilt(b)
		_, _ = e.run("", "runtime", "service", "stop", "--json")
	})
	var up struct {
		Errors []string `json:"errors"`
	}
	if err := e.runJSON(b.Root, &up, "up", "--json"); err != nil || len(up.Errors) != 0 {
		t.Fatalf("up = %+v, err=%v", up, err)
	}
	status := e.waitStatus(b.Root, func(status statusDoc) bool {
		return status.serviceRunning("api", b.Port, true) && status.AttnPath == ""
	})
	_ = status
	var logs struct {
		Lines []string `json:"lines"`
	}
	if err := e.runJSON(b.Root, &logs, "logs", "api", "--once", "--json"); err != nil {
		t.Fatalf("ordinary log read: %v", err)
	}
	if got := e.waitStatus(b.Root, func(status statusDoc) bool { return status.AttnPath == "" }); got.AttnPath != "" {
		t.Fatal("ordinary log read created attention")
	}

	extraPort := freePort()
	undeclaredProcess := startUndeclaredListener(t, e, b.Root, extraPort)
	// The port must become visible as a fact while no artifact appears. The
	// wait is on the fact, not the artifact: a passing run is one where this
	// predicate is satisfiable at all.
	undeclared := e.waitStatus(b.Root, func(status statusDoc) bool {
		return portsContain(status, extraPort)
	})
	if undeclared.AttnPath != "" {
		artifact := readAttnDoc(t, undeclared)
		t.Fatalf("an undeclared listener created a handoff: %+v", artifact.Events)
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
		return ok && !api.Running && status.AttnPath == ""
	}); got.AttnPath != "" {
		t.Fatal("normal stop left an attention request")
	}
}

// TestAttentionManifestConflictIsNotActionable makes a malformed duplicate
// service declaration after a clean run. Status must fail at the manifest
// boundary; it must not turn a parser/configuration conflict into a guessed
// service action or a new attention artifact.
func TestAttentionManifestConflictIsNotActionable(t *testing.T) {
	e := newEnv(t)
	e.runRuntimeService()
	enableAttentionIn(t, e)
	b, err := APIServerAndWorker(filepath.Join(e.home, "attention-conflict"))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() {
		e.cleanupBuilt(b)
		_, _ = e.run("", "runtime", "service", "stop", "--json")
	})
	var up struct {
		Errors []string `json:"errors"`
	}
	if err := e.runJSON(b.Root, &up, "up", "--json"); err != nil || len(up.Errors) != 0 {
		t.Fatalf("up = %+v, err=%v", up, err)
	}
	clean := e.waitStatus(b.Root, func(status statusDoc) bool {
		return status.AttnPath == ""
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
	// Stop the runtime service so status must parse the changed manifest rather than
	// serving the old indexed group snapshot.
	if _, err := e.run("", "runtime", "service", "stop", "--json"); err != nil {
		t.Fatalf("runtime service stop: %v", err)
	}
	stdout, stderr, err := e.runCapture(b.Root, "status", "--json", "--no-mark")
	if err == nil || len(stdout) != 0 || !strings.Contains(string(stderr), "duplicate") {
		t.Fatalf("conflicting manifest status: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if _, err := os.Stat(clean.AttnPath); clean.AttnPath != "" && err == nil {
		t.Fatalf("manifest conflict left an actionable artifact at %s", clean.AttnPath)
	}
}
