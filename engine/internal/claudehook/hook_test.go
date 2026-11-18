package claudehook

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/sheathedsharp/option-berth/engine/internal/attn"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/runner"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Adapter, *atomic.Int32) {
	t.Helper()
	n := new(atomic.Int32)
	a := &Adapter{Home: t.TempDir(), Root: t.TempDir(), Identity: "config-one", Task: model.TaskContext{Task: Summary, RunIDs: []string{"worker-1"}, AuthorizedOptions: []string{"inspect_logs"}}, Duration: time.Second}
	a.Consent = func() error { return nil }
	a.Prepare = func(_ context.Context, task model.TaskContext) (string, error) {
		if task.Task != Summary {
			t.Error("raw prompt used")
		}
		return `{"runs":[{"run_id":"worker-1"}]}`, nil
	}
	a.Fresh = func(_ context.Context, _ model.TaskContext, f Feedback) bool { return f.EventID == "event-1" }
	a.Observe = func(ctx context.Context, _ model.TaskContext, emit func(runner.Output) error) error {
		n.Add(1)
		defer n.Add(-1)
		relevance := .9
		err := emit(runner.Output{Status: "continue", EventID: "event-1", AttnRevision: "revision-1", FreshUntil: time.Now().Add(time.Minute).Format(time.RFC3339Nano), Assessment: &attn.Assessment{TaskRelevant: &relevance}, Decision: &runner.HILDecision{NextOption: "inspect_logs", Reason: "confident_read_only_step"}, Request: &runner.HILRequest{Events: []attn.Event{{Kind: attn.EventServiceFailed, Service: "worker", RunID: "worker-1"}}, Evidence: []attn.Evidence{{LogPath: "NEVER-DELIVER-LOG-PATH"}}}})
		if err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}
	a.Spawn = func(key, gen string) error {
		clone := *a
		go func() { _ = clone.Run(context.Background(), key, gen) }()
		return nil
	}
	t.Cleanup(func() {
		ctx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = a.stop(ctx, Key(a.Root, "s1"))
	})
	return a, n
}
func event(a *Adapter, name string) Input { return Input{Event: name, Session: "s1", Cwd: a.Root} }
func wait(t *testing.T, fn func() bool) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for !fn() {
		if time.Now().After(until) {
			t.Fatal("condition never reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func submit(t *testing.T, a *Adapter) {
	t.Helper()
	ctx, c := context.WithTimeout(context.Background(), time.Second)
	defer c()
	r, e := a.Handle(ctx, event(a, "UserPromptSubmit"))
	if e != nil || !strings.Contains(r.Message, "observing") || r.Specific == nil || !strings.Contains(r.Specific.Text, "worker-1") {
		t.Fatalf("not attached: %+v %v", r, e)
	}
	wait(t, func() bool { s, _ := a.load(Key(a.Root, "s1")); return s.Pending != nil })
}
func TestNativeStopConsumesRelevantReadonlyFeedbackOnce(t *testing.T) {
	a, n := fixture(t)
	submit(t, a)
	r, e := a.Handle(context.Background(), event(a, "Stop"))
	if e != nil || r.Decision != "block" || !strings.Contains(r.Reason, "worker-1") || strings.Contains(r.Reason, "NEVER-DELIVER") {
		t.Fatalf("not delivered: %+v %v", r, e)
	}
	if n.Load() != 0 {
		t.Fatal("observer not joined before feedback")
	}
	r, e = a.Handle(context.Background(), event(a, "Stop"))
	if e != nil || r.Decision != "" {
		t.Fatal("duplicate delivery")
	}
	s, _ := a.load(Key(a.Root, "s1"))
	if !s.Consumed || s.Active || !s.Finished || s.Pending != nil {
		t.Fatalf("lease retained: %+v", s)
	}
}
func TestNativeStopGuardsAndCleanup(t *testing.T) {
	for _, name := range []string{"StopFailure", "SessionEnd", "StopActive", "Revoked", "ConfigChanged", "Stale"} {
		t.Run(name, func(t *testing.T) {
			a, n := fixture(t)
			submit(t, a)
			in := event(a, name)
			if name == "StopActive" {
				in.Event = "Stop"
				in.StopActive = true
			}
			if name == "Revoked" {
				in.Event = "Stop"
				a.Consent = func() error { return errors.New("consent_revoked") }
			}
			if name == "ConfigChanged" {
				in.Event = "Stop"
				a.Identity = "new-config"
			}
			if name == "Stale" {
				in.Event = "Stop"
				a.Fresh = func(context.Context, model.TaskContext, Feedback) bool { return false }
			}
			r, e := a.Handle(context.Background(), in)
			if e != nil || r.Decision != "" || n.Load() != 0 {
				t.Fatalf("unsafe feedback or leak: %+v %v n=%d", r, e, n.Load())
			}
		})
	}
}
func TestLeaseExpiresWithoutSessionEnd(t *testing.T) {
	a, n := fixture(t)
	a.Duration = 140 * time.Millisecond
	submit(t, a)
	wait(t, func() bool { return n.Load() == 0 })
	r, e := a.Handle(context.Background(), event(a, "Stop"))
	if e != nil || r.Decision != "" || !strings.Contains(r.Message, "expired") {
		t.Fatalf("expired task resumed: %+v %v", r, e)
	}
}
func TestNewTaskGenerationJoinsPreviousAndDoesNotReplay(t *testing.T) {
	a, n := fixture(t)
	submit(t, a)
	before, _ := a.load(Key(a.Root, "s1"))
	submit(t, a)
	after, _ := a.load(Key(a.Root, "s1"))
	if before.Generation == after.Generation || n.Load() != 1 {
		t.Fatal("task generation not isolated")
	}
}
func TestKilledObserverCannotRestorePendingFeedback(t *testing.T) {
	a, _ := fixture(t)
	key := Key(a.Root, "s1")
	s := Lease{Generation: newGeneration(), Expires: time.Now().Add(time.Minute), Active: true, Status: "observing", Task: a.Task, Pending: &Feedback{Body: "old"}}
	if e := a.save(key, s); e != nil {
		t.Fatal(e)
	}
	if e := a.Run(context.Background(), key, s.Generation); e == nil {
		t.Fatal("crashed lease resumed")
	}
	r, e := a.Handle(context.Background(), event(a, "Stop"))
	if e != nil || r.Decision != "" {
		t.Fatal("crashed result delivered")
	}
}
func TestMissingScopeAuthorizationAndEditAreVisible(t *testing.T) {
	for _, why := range []string{"scope_required", "task_authorization_required", "pure_edit"} {
		t.Run(why, func(t *testing.T) {
			a, n := fixture(t)
			switch why {
			case "scope_required":
				a.Task.RunIDs = nil
			case "task_authorization_required":
				a.Task.AuthorizedOptions = nil
			case "pure_edit":
				a.EditOnly = true
			}
			r, e := a.Handle(context.Background(), event(a, "UserPromptSubmit"))
			if e != nil || !strings.Contains(r.Message, why) || n.Load() != 0 {
				t.Fatalf("silent or unsafe: %+v %v", r, e)
			}
		})
	}
}
func TestHookInputIgnoresPromptTranscriptAndAnswer(t *testing.T) {
	in, e := Decode(strings.NewReader(`{"hook_event_name":"UserPromptSubmit","session_id":"s1","cwd":"/project","prompt":"PROMPT-SECRET","transcript_path":"/must/not/read","last_assistant_message":"ANSWER-SECRET"}`))
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(in)
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "transcript") {
		t.Fatal("private fields retained")
	}
}
func TestParallelWorktreesAndSessionsDoNotCross(t *testing.T) {
	a, _ := fixture(t)
	submit(t, a)
	b := *a
	b.Root = t.TempDir()
	r, e := b.Handle(context.Background(), Input{Event: "Stop", Session: "s1", Cwd: b.Root})
	if e != nil || r.Decision != "" {
		t.Fatal("other worktree received task")
	}
	r, e = a.Handle(context.Background(), Input{Event: "Stop", Session: "other", Cwd: a.Root})
	if e != nil || r.Decision != "" {
		t.Fatal("other session received task")
	}
}
func TestScopeChangeCannotConsumeOldResult(t *testing.T) {
	a, _ := fixture(t)
	submit(t, a)
	a.Task.RunIDs = []string{"new-run"}
	r, e := a.Handle(context.Background(), event(a, "Stop"))
	if e != nil || r.Decision != "" || !strings.Contains(r.Message, "task_scope_changed") {
		t.Fatalf("scope changed: %+v %v", r, e)
	}
}
func TestPrivateCursorDoesNotPersistSessionOrRuntimeLogs(t *testing.T) {
	a, _ := fixture(t)
	submit(t, a)
	data, e := os.ReadFile(filepath.Join(a.dir(Key(a.Root, "s1")), "lease.json"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(data), "NEVER-DELIVER") || strings.Contains(string(data), `"session_id"`) {
		t.Fatal("privacy violation")
	}
}

func TestRuntimeFactsDoNotDependOnAttentionPermission(t *testing.T) {
	a, n := fixture(t)
	a.Consent = func() error { return errors.New("attention_off") }
	r, e := a.Handle(context.Background(), event(a, "UserPromptSubmit"))
	if e != nil || !strings.Contains(r.Message, "attention_off") || r.Specific == nil || !strings.Contains(r.Specific.Text, "worker-1") || n.Load() != 0 {
		t.Fatalf("optional Attention hid core context: %+v %v", r, e)
	}
}
func TestWorkerCancellationDoesNotLeaveConsumableFeedback(t *testing.T) {
	a, n := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	a.Spawn = func(key, gen string) error { clone := *a; go func() { _ = clone.Run(ctx, key, gen) }(); return nil }
	submit(t, a)
	cancel()
	wait(t, func() bool { return n.Load() == 0 })
	time.Sleep(20 * time.Millisecond)
	r, e := a.Handle(context.Background(), event(a, "Stop"))
	if e != nil || r.Decision != "" {
		t.Fatalf("canceled worker delivered a result: %+v %v", r, e)
	}
}

func TestObserverCannotRebindLeaseToAnotherWorktree(t *testing.T) {
	a, _ := fixture(t)
	key := Key(a.Root, "s1")
	lease := Lease{WorktreeID: model.WorktreeID(a.Root), Generation: newGeneration(), Expires: time.Now().Add(time.Second), Active: true, Status: "starting", Task: a.Task, Consent: a.Identity}
	if err := a.save(key, lease); err != nil {
		t.Fatal(err)
	}
	other := *a
	other.Root = t.TempDir()
	called := false
	other.Observe = func(context.Context, model.TaskContext, func(runner.Output) error) error { called = true; return nil }
	_ = other.Run(context.Background(), key, lease.Generation)
	if called {
		t.Fatal("lease rebound to another worktree")
	}
}
