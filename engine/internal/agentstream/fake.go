package agentstream

import (
	"fmt"
	"strings"
)

// fakeDriver emits a deterministic scripted turn for tests and CI.
type fakeDriver struct {
	provider   string
	worktree   string
	resumeFile string
	started    bool
	busy       bool
	turns      int
	holdTurn   string
}

func (f *fakeDriver) turnStart(b *bridge, id, text string) error {
	if strings.TrimSpace(text) == "" {
		b.emitError(id, "invalid", "turn text must be non-empty")
		return nil
	}
	if f.busy {
		b.emitError(id, "busy", "a turn is already running")
		return nil
	}
	b.emitAck(id)
	if !f.started {
		f.started = true
		b.emit(map[string]any{
			"v": 1, "type": "session.started",
			"provider": f.provider, "protocol": "fake", "worktree": f.worktree,
			"provider_session_id": "fake-session-1",
			"capabilities":        []string{"interrupt", "approvals"},
		})
	}
	f.turns++
	turn := fmt.Sprintf("t%d", f.turns)
	f.busy = true
	b.emit(map[string]any{"v": 1, "type": "turn.started", "turn": turn, "input": text})
	if text == "hold" {
		f.holdTurn = turn
		return nil
	}
	if text == "approve" {
		b.emit(map[string]any{
			"v": 1, "type": "request.opened", "turn": turn,
			"request": map[string]any{
				"id": "r1", "kind": "command", "command": "rm -rf .cache", "cwd": f.worktree,
				"reason": "cleanup", "decisions": []string{"allow", "allow_session", "deny"},
			},
		})
		f.holdTurn = turn
		return nil
	}
	f.busy = false
	b.emit(map[string]any{"v": 1, "type": "turn.completed", "turn": turn, "outcome": "completed"})
	return nil
}

func (f *fakeDriver) turnInterrupt(b *bridge, id string) error {
	if !f.busy {
		b.emitError(id, "idle", "no turn to interrupt")
		return nil
	}
	b.emitAck(id)
	if f.holdTurn != "" {
		turn := f.holdTurn
		f.holdTurn = ""
		f.busy = false
		b.emit(map[string]any{"v": 1, "type": "turn.completed", "turn": turn, "outcome": "interrupted"})
		return nil
	}
	f.busy = false
	return nil
}

func (f *fakeDriver) requestRespond(b *bridge, id, request, decision string) error {
	if request == "" || decision == "" {
		b.emitError(id, "invalid", "request and decision are required")
		return nil
	}
	switch decision {
	case "allow", "allow_session", "deny":
	default:
		b.emitError(id, "invalid", "unknown decision")
		return nil
	}
	b.emitAck(id)
	b.emit(map[string]any{"v": 1, "type": "request.closed", "request": request, "decision": decision, "by": "client"})
	if f.holdTurn != "" {
		turn := f.holdTurn
		f.holdTurn = ""
		f.busy = false
		b.emit(map[string]any{"v": 1, "type": "turn.completed", "turn": turn, "outcome": "completed"})
	}
	return nil
}

func (f *fakeDriver) sessionEnd(b *bridge) {
	if !f.started {
		return
	}
	b.emit(map[string]any{"v": 1, "type": "session.ended", "reason": "closed", "exit_code": 0})
}
