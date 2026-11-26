package claudehook

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
)

// PostToolUse is the mid-turn half of the native hook set. Three properties
// have to hold: it never blocks the turn, it never touches the observer lease,
// and it says nothing when there is nothing new.

func TestDecodeAcceptsPostToolUse(t *testing.T) {
	in, err := Decode(strings.NewReader(`{"hook_event_name":"PostToolUse","session_id":"s1","cwd":"/project","tool_name":"Bash","tool_response":"SECRET-OUTPUT"}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.Event != "PostToolUse" || in.Cwd != "/project" {
		t.Fatalf("decoded = %+v", in)
	}
	body, _ := json.Marshal(in)
	if strings.Contains(string(body), "SECRET-OUTPUT") || strings.Contains(string(body), "tool_response") {
		t.Fatalf("the decoding retained tool output: %s", body)
	}
}

func TestPostToolUseDeliversContextWithoutTouchingTheLease(t *testing.T) {
	a, n := fixture(t)
	submit(t, a)
	before, err := a.load(Key(a.Root, "s1"))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	a.MidTurn = func(context.Context) (MidTurnBrief, bool) {
		calls.Add(1)
		return MidTurnBrief{Revision: "rev-1", Text: "Oberth Runtime attention: api crashed with exit code 1."}, true
	}

	r, e := a.Handle(context.Background(), event(a, "PostToolUse"))
	if e != nil {
		t.Fatalf("post-tool hook failed: %v", e)
	}
	if r.Decision != "" || r.Reason != "" {
		t.Fatalf("a mid-turn note tried to block the turn: %+v", r)
	}
	if r.Specific == nil || r.Specific.Event != "PostToolUse" || !strings.Contains(r.Specific.Text, "api crashed") {
		t.Fatalf("mid-turn context = %+v", r)
	}
	if calls.Load() != 1 {
		t.Fatalf("mid-turn callback calls = %d, want 1", calls.Load())
	}
	// The lease belongs to the prompt. A tool call neither renews it nor ends
	// it, and the observer keeps running.
	after, err := a.load(Key(a.Root, "s1"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Generation != before.Generation || !after.Active || after.Consumed || after.Status != before.Status {
		t.Fatalf("post-tool hook changed the lease: before=%+v after=%+v", before, after)
	}
	if n.Load() != 1 {
		t.Fatalf("observer goroutines = %d, want 1", n.Load())
	}
}

func TestPostToolUseWithNothingNewIsEmpty(t *testing.T) {
	a, _ := fixture(t)
	// No callback at all: an older wiring must not fail a tool call.
	r, err := a.Handle(context.Background(), Input{Event: "PostToolUse", Session: "s1", Cwd: a.Root})
	if err != nil || r.Decision != "" || r.Specific != nil || r.Message != "" {
		t.Fatalf("response without a callback = %+v, err=%v", r, err)
	}
	a.MidTurn = func(context.Context) (MidTurnBrief, bool) { return MidTurnBrief{}, false }
	r, err = a.Handle(context.Background(), Input{Event: "PostToolUse", Session: "s1", Cwd: a.Root})
	if err != nil || r.Decision != "" || r.Specific != nil || r.Message != "" {
		t.Fatalf("response with nothing new = %+v, err=%v", r, err)
	}
	// An empty text is also nothing: a blank additionalContext would be noise.
	a.MidTurn = func(context.Context) (MidTurnBrief, bool) { return MidTurnBrief{Revision: "r"}, true }
	r, _ = a.Handle(context.Background(), Input{Event: "PostToolUse", Session: "s1", Cwd: a.Root})
	if r.Specific != nil {
		t.Fatalf("an empty brief produced context: %+v", r)
	}
}

func TestPostToolUseTextIsBounded(t *testing.T) {
	a, _ := fixture(t)
	a.MidTurn = func(context.Context) (MidTurnBrief, bool) {
		return MidTurnBrief{Revision: "rev", Text: strings.Repeat("x", midTurnMaxRunes*3)}, true
	}
	r, err := a.Handle(context.Background(), Input{Event: "PostToolUse", Session: "s1", Cwd: a.Root})
	if err != nil || r.Specific == nil {
		t.Fatalf("response = %+v err=%v", r, err)
	}
	if got := len([]rune(r.Specific.Text)); got != midTurnMaxRunes {
		t.Fatalf("text = %d runes, want %d", got, midTurnMaxRunes)
	}
	body, _ := json.Marshal(r)
	if !strings.Contains(string(body), `"hookEventName":"PostToolUse"`) {
		t.Fatalf("wire shape = %s", body)
	}
}

func TestPostToolUseOnAnotherWorktreeSaysNothing(t *testing.T) {
	a, _ := fixture(t)
	a.MidTurn = func(context.Context) (MidTurnBrief, bool) {
		t.Error("mid-turn callback ran for a different worktree")
		return MidTurnBrief{Text: "leak"}, true
	}
	r, err := a.Handle(context.Background(), Input{Event: "PostToolUse", Session: "s1", Cwd: t.TempDir()})
	if err != nil || r.Specific != nil || !strings.Contains(r.Message, "worktree_mismatch") {
		t.Fatalf("response = %+v err=%v", r, err)
	}
}

func TestPostToolUseSilentUnderEditOnly(t *testing.T) {
	a, _ := fixture(t)
	a.EditOnly = true
	a.MidTurn = func(context.Context) (MidTurnBrief, bool) {
		t.Error("mid-turn callback ran in pure-edit mode")
		return MidTurnBrief{Revision: "rev-1", Text: "should not appear"}, true
	}
	r, err := a.Handle(context.Background(), Input{Event: "PostToolUse", Session: "s1", Cwd: a.Root})
	if err != nil || r.Specific != nil || r.Decision != "" || r.Message != "" {
		t.Fatalf("pure-edit PostToolUse response = %+v err=%v", r, err)
	}
}
