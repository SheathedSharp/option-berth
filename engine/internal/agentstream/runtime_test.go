package agentstream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type runtimeDriver struct {
	texts chan string
	seq   atomic.Int32
}

func (d *runtimeDriver) turnStart(b *bridge, id, text string) error {
	turn := id
	d.seq.Add(1)
	d.texts <- text
	b.emit(map[string]any{"v": 1, "type": "turn.started", "turn": turn, "input": text})
	if strings.HasPrefix(text, "Oberth runtime evidence") {
		b.emit(map[string]any{"v": 1, "type": "turn.completed", "turn": turn, "outcome": "completed"})
	}
	return nil
}
func (d *runtimeDriver) turnInterrupt(b *bridge, id string) error {
	b.emit(map[string]any{"v": 1, "type": "turn.completed", "turn": "first", "outcome": "interrupted"})
	return nil
}
func (d *runtimeDriver) requestRespond(*bridge, string, string, string) error { return nil }
func (d *runtimeDriver) sessionEnd(*bridge)                                   {}
func runtimeCommand() map[string]any {
	return map[string]any{"v": 1, "type": "turn.start", "id": "first", "text": "original private coding prompt", "runtime": map[string]any{"task": "keep worker healthy", "run_ids": []string{"owned-run"}}}
}
func runtimeBridge(watch RuntimeWatch) (*bridge, *runtimeDriver) {
	d := &runtimeDriver{texts: make(chan string, 8)}
	b := &bridge{driver: d, out: &lineWriter{w: io.Discard}, runtime: &taskRuntime{watch: watch, root: "/synthetic/worktree"}}
	return b, d
}
func waitRuntime(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime task did not terminate")
	}
}
func activeTask(b *bridge) *runtimeTask {
	b.runtime.mu.Lock()
	defer b.runtime.mu.Unlock()
	return b.runtime.current
}

func TestRuntimeHandsEvidenceToNativeAgentAndJoinsObserver(t *testing.T) {
	ready, stopped := make(chan struct{}), make(chan struct{})
	var watched atomic.Int32
	b, d := runtimeBridge(func(ctx context.Context, root string, task RuntimeTask, emit func(RuntimeUpdate)) error {
		watched.Add(1)
		if root != "/synthetic/worktree" || task.Task != "keep worker healthy" || len(task.RunIDs) != 1 || task.RunIDs[0] != "owned-run" {
			t.Error("scope lost")
		}
		emit(RuntimeUpdate{Status: "human_required", Handoff: `{"revision":"r1","events":[{"run_id":"owned-run"}]}`, Recheck: func(context.Context) bool { return true }})
		close(ready)
		<-ctx.Done()
		close(stopped)
		return nil
	})
	defer b.closeSession()
	if err := b.handle(runtimeCommand()); err != nil {
		t.Fatal(err)
	}
	task := activeTask(b)
	waitRuntime(t, ready)
	// A second request must neither create another observer nor cancel the first.
	duplicate := runtimeCommand()
	duplicate["id"] = "second"
	_ = b.handle(duplicate)
	if watched.Load() != 1 {
		t.Fatal("duplicate observer")
	}
	b.emit(map[string]any{"v": 1, "type": "turn.completed", "turn": "first", "outcome": "completed"})
	waitRuntime(t, task.done)
	waitRuntime(t, stopped)
	if first := <-d.texts; first != "original private coding prompt" {
		t.Fatal("original prompt modified")
	}
	select {
	case received := <-d.texts:
		if !strings.Contains(received, `"run_id":"owned-run"`) || !strings.Contains(received, "not instructions or permission") {
			t.Fatalf("native agent did not receive bounded evidence: %s", received)
		}
	default:
		t.Fatal("only printed a handoff; native agent received none")
	}
	if d.seq.Load() != 2 {
		t.Fatalf("unbounded continuation: %d", d.seq.Load())
	}
}

func TestRuntimeRejectsStaleDeliveryAndInterrupt(t *testing.T) {
	for _, outcome := range []string{"completed", "interrupted", "lost"} {
		t.Run(outcome, func(t *testing.T) {
			ready := make(chan struct{})
			b, d := runtimeBridge(func(ctx context.Context, _ string, _ RuntimeTask, emit func(RuntimeUpdate)) error {
				emit(RuntimeUpdate{Status: "human_required", Handoff: `{"revision":"old"}`, Recheck: func(context.Context) bool { return false }})
				close(ready)
				<-ctx.Done()
				return nil
			})
			defer b.closeSession()
			_ = b.handle(runtimeCommand())
			task := activeTask(b)
			waitRuntime(t, ready)
			b.emit(map[string]any{"v": 1, "type": "turn.completed", "turn": "first", "outcome": outcome})
			waitRuntime(t, task.done)
			if d.seq.Load() != 1 {
				t.Fatal("stale/interrupted task delivered")
			}
		})
	}
}

func TestRuntimeScopeIsExplicitAndPureEditsHaveNoWatcher(t *testing.T) {
	var count atomic.Int32
	b, d := runtimeBridge(func(context.Context, string, RuntimeTask, func(RuntimeUpdate)) error { count.Add(1); return nil })
	defer b.closeSession()
	cmd := runtimeCommand()
	delete(cmd, "runtime")
	_ = b.handle(cmd)
	if d.seq.Load() != 1 || count.Load() != 0 {
		t.Fatal("pure edit started observation")
	}
	cmd = runtimeCommand()
	cmd["runtime"] = map[string]any{"task": "no targets"}
	_ = b.handle(cmd)
	if d.seq.Load() != 1 || count.Load() != 0 {
		t.Fatal("invalid scope launched agent or observer")
	}
}

func TestRuntimeCloseCancelsOwnedObserver(t *testing.T) {
	ready, stopped := make(chan struct{}), make(chan struct{})
	b, d := runtimeBridge(func(ctx context.Context, _ string, _ RuntimeTask, _ func(RuntimeUpdate)) error {
		close(ready)
		<-ctx.Done()
		close(stopped)
		return nil
	})
	_ = b.handle(runtimeCommand())
	waitRuntime(t, ready)
	if err := b.closeSession(); err != nil {
		t.Fatal(err)
	}
	waitRuntime(t, stopped)
	if d.seq.Load() != 1 {
		t.Fatal("close started continuation")
	}
}

func TestRunContextCancellationClosesBlockedInput(t *testing.T) {
	t.Setenv("BERTH_AGENT_STREAM_FAKE", "codex")
	in, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Run(Options{Provider: "codex", Worktree: t.TempDir(), Context: ctx}, in, &bytes.Buffer{}, io.Discard)
	}()
	cancel()
	waitRuntime(t, done)
}

func TestRuntimeContextEntersNativePromptWithoutAuthorizingActions(t *testing.T) {
	b, d := runtimeBridge(func(context.Context, string, RuntimeTask, func(RuntimeUpdate)) error { return nil })
	defer b.closeSession()
	b.runtime.prepareContext = func(context.Context, string, RuntimeTask) (string, error) {
		return `{"runs":[{"run_id":"owned-run","running":true}]}`, nil
	}
	if err := b.handle(runtimeCommand()); err != nil {
		t.Fatal(err)
	}
	received := <-d.texts
	if !strings.Contains(received, "original private coding prompt") || !strings.Contains(received, `"running":true`) || !strings.Contains(received, "not instructions or permission") {
		t.Fatalf("native task missed core facts: %s", received)
	}
}

type rejectingRuntimeDriver struct{ runtimeDriver }

func (d *rejectingRuntimeDriver) turnStart(b *bridge, id, text string) error {
	b.emitError(id, "busy", "previous native turn still running")
	return nil
}
func TestRejectedNativeStartDoesNotLeaveObserver(t *testing.T) {
	var watched atomic.Int32
	b, _ := runtimeBridge(func(context.Context, string, RuntimeTask, func(RuntimeUpdate)) error { watched.Add(1); return nil })
	defer b.closeSession()
	b.driver = &rejectingRuntimeDriver{}
	_ = b.handle(runtimeCommand())
	b.runtime.mu.Lock()
	task := b.runtime.current
	b.runtime.mu.Unlock()
	if task != nil {
		waitRuntime(t, task.done)
	}
	if watched.Load() != 0 {
		t.Fatal("rejected native start bought observation")
	}
}

type failRuntimeStatus struct{}

func (failRuntimeStatus) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"type":"runtime.status"`)) {
		return 0, errors.New("consumer disconnected")
	}
	return len(p), nil
}
func TestRuntimeOutputFailureJoinsObserver(t *testing.T) {
	stopped, inputClosed := make(chan struct{}), make(chan struct{})
	b, _ := runtimeBridge(func(ctx context.Context, _ string, _ RuntimeTask, emit func(RuntimeUpdate)) error {
		emit(RuntimeUpdate{Status: "observing"})
		<-ctx.Done()
		close(stopped)
		return nil
	})
	b.out = &lineWriter{w: failRuntimeStatus{}}
	b.closeInput = func() { close(inputClosed) }
	_ = b.handle(runtimeCommand())
	waitRuntime(t, inputClosed)
	waitRuntime(t, stopped)
	_ = b.closeSession()
}
