package agentstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type runtimeReceipt struct {
	mu   sync.Mutex
	data bytes.Buffer
	done chan struct{}
	once sync.Once
}

func (w *runtimeReceipt) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.data.Write(p)
	if bytes.Contains(p, []byte("native_continuation_completed")) {
		w.once.Do(func() { close(w.done) })
	}
	return n, err
}
func (w *runtimeReceipt) text() string { w.mu.Lock(); defer w.mu.Unlock(); return w.data.String() }

func TestRuntimeEvidenceReachesNativeCodexProtocol(t *testing.T) {
	// Compile only our fake process; no installed agent, account, or API key.
	t.Setenv("BERTH_AGENT_STREAM_FAKE", "")
	binary := filepath.Join(t.TempDir(), "fakecodex")
	build := exec.Command("go", "build", "-o", binary, "./testdata/fakecodex")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fake build: %v %s", err, out)
	}
	input, writer := io.Pipe()
	defer writer.Close()
	output := &runtimeReceipt{done: make(chan struct{})}
	finished := make(chan struct{})
	var runErr error
	go func() {
		defer close(finished)
		runErr = Run(Options{Provider: "codex", Worktree: t.TempDir(), LookPath: func(string) (string, error) { return binary, nil }, RuntimeWatch: func(ctx context.Context, _ string, _ RuntimeTask, emit func(RuntimeUpdate)) error {
			emit(RuntimeUpdate{Status: "human_required", Handoff: `{"revision":"native-r1","events":[{"run_id":"task-owned-worker"}]}`, Recheck: func(context.Context) bool { return true }})
			<-ctx.Done()
			return nil
		}}, input, output, io.Discard)
	}()
	cmd := runtimeCommand()
	cmd["text"] = "runtime-hold"
	cmd["runtime"] = map[string]any{"task": "keep task worker healthy", "run_ids": []string{"task-owned-worker"}}
	if err := json.NewEncoder(writer).Encode(cmd); err != nil {
		t.Fatal(err)
	}
	waitRuntime(t, output.done)
	_ = writer.Close()
	waitRuntime(t, finished)
	if runErr != nil {
		t.Fatal(runErr)
	}
	text := output.text()
	if !strings.Contains(text, "native-received:Oberth runtime evidence") || !strings.Contains(text, "task-owned-worker") {
		t.Fatalf("native provider never consumed evidence: %s", text)
	}
	if strings.Count(text, `"type":"turn.started"`) != 2 {
		t.Fatalf("expected original and bounded continuation: %s", text)
	}
}
