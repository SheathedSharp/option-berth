package agentstream

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/internal/agentlaunch"
)

type claudeLive struct {
	worktree   string
	resumeFile string
	lookPath   func(string) (string, error)

	mu           sync.Mutex
	cmd          *exec.Cmd
	childIn      io.WriteCloser
	rpc          *jsonrpcConn
	started      bool
	busy         bool
	turnSeq      int
	currentTurn  string
	readDone     chan struct{}
	readLoopLive bool
}

func newClaudeLive(worktree, resumeFile string, lookPath func(string) (string, error)) *claudeLive {
	return &claudeLive{worktree: worktree, resumeFile: resumeFile, lookPath: lookPath}
}

func (c *claudeLive) turnStart(b *bridge, id, text string) error {
	if strings.TrimSpace(text) == "" {
		b.emitError(id, "invalid", "turn text must be non-empty")
		return nil
	}
	c.mu.Lock()
	if c.busy {
		c.mu.Unlock()
		b.emitError(id, "busy", "a turn is already running")
		return nil
	}
	c.mu.Unlock()
	if err := c.ensureSession(b); err != nil {
		b.emitError(id, "unsupported", err.Error())
		return nil
	}
	b.emitAck(id)
	c.mu.Lock()
	c.turnSeq++
	turn := fmt.Sprintf("t%d", c.turnSeq)
	c.currentTurn = turn
	c.busy = true
	c.mu.Unlock()
	b.emit(map[string]any{"v": 1, "type": "turn.started", "turn": turn, "input": text})
	if err := c.writeLine(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{{"type": "text", "text": text}},
		},
	}); err != nil {
		c.mu.Lock()
		c.busy = false
		c.mu.Unlock()
		b.emit(map[string]any{"v": 1, "type": "turn.completed", "turn": turn, "outcome": "lost"})
	}
	return nil
}

func (c *claudeLive) turnInterrupt(b *bridge, id string) error {
	c.mu.Lock()
	if !c.busy {
		c.mu.Unlock()
		b.emitError(id, "idle", "no turn to interrupt")
		return nil
	}
	c.mu.Unlock()
	b.emitAck(id)
	_ = c.writeLine(map[string]any{
		"type":    "control_request",
		"request": map[string]any{"subtype": "interrupt"},
	})
	return nil
}

func (c *claudeLive) requestRespond(b *bridge, id, request, decision string) error {
	if request == "" || decision == "" {
		b.emitError(id, "invalid", "request and decision are required")
		return nil
	}
	var allow bool
	switch decision {
	case "allow", "allow_session":
		allow = true
	case "deny":
		allow = false
	default:
		b.emitError(id, "invalid", "unknown decision")
		return nil
	}
	b.emitAck(id)
	_ = c.writeLine(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"request_id": request,
			"subtype":    "can_use_tool",
			"response":   map[string]any{"allow": allow},
		},
	})
	b.emit(map[string]any{"v": 1, "type": "request.closed", "request": request, "decision": decision, "by": "client"})
	return nil
}

func (c *claudeLive) sessionEnd(b *bridge) {
	c.interruptIfBusy()
	c.shutdown(b, "closed", 0)
}

func (c *claudeLive) interruptIfBusy() {
	c.mu.Lock()
	busy := c.busy
	c.mu.Unlock()
	if !busy {
		return
	}
	_ = c.writeLine(map[string]any{
		"type":    "control_request",
		"request": map[string]any{"subtype": "interrupt"},
	})
	deadline := time.Now().Add(interruptWait)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		idle := !c.busy
		c.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (c *claudeLive) ensureSession(b *bridge) error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	executable, err := c.lookPath("claude")
	if err != nil {
		return fmt.Errorf("claude is not installed")
	}
	args := []string{
		"-p", "--input-format", "stream-json", "--output-format", "stream-json",
		"--verbose", "--include-partial-messages", "--permission-prompt-tool", "stdio",
	}
	if c.resumeFile != "" {
		ref, err := agentlaunch.InspectResume("claude", c.resumeFile, c.worktree)
		if err != nil {
			return err
		}
		args = append(args, "--resume", ref.SessionID)
	}
	cmd := exec.Command(executable, args...)
	cmd.Dir = c.worktree
	setProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("claude stream failed to start")
	}
	rpc := newJSONRPCConn(stdout, stdin)
	c.mu.Lock()
	c.cmd = cmd
	c.childIn = stdin
	c.rpc = rpc
	c.readDone = make(chan struct{})
	c.mu.Unlock()
	if err := c.waitInit(); err != nil {
		c.shutdown(b, "lost", 1)
		return err
	}
	c.mu.Lock()
	c.readLoopLive = true
	c.mu.Unlock()
	go c.readLoop(b)
	c.mu.Lock()
	c.started = true
	c.mu.Unlock()
	b.emit(map[string]any{
		"v": 1, "type": "session.started",
		"provider": "claude", "protocol": "claude-stream-json", "worktree": c.worktree,
		"provider_session_id": "claude-session",
		"capabilities":        []string{"interrupt", "approvals"},
	})
	return nil
}

func (c *claudeLive) waitInit() error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msg, err := c.rpc.read()
		if err != nil {
			return err
		}
		if typ, _ := msg["type"].(string); typ == "system" {
			sub, _ := msg["subtype"].(string)
			if sub == "init" {
				return nil
			}
		}
	}
	return fmt.Errorf("claude stream init timed out")
}

func (c *claudeLive) writeLine(v map[string]any) error {
	c.mu.Lock()
	rpc := c.rpc
	c.mu.Unlock()
	if rpc == nil {
		return fmt.Errorf("claude stream is not connected")
	}
	return rpc.write(v)
}

func (c *claudeLive) readLoop(b *bridge) {
	c.mu.Lock()
	rpc := c.rpc
	done := c.readDone
	c.mu.Unlock()
	if done != nil {
		defer close(done)
	}
	if rpc == nil {
		return
	}
	for {
		msg, err := rpc.read()
		if err != nil {
			c.mu.Lock()
			busy := c.busy
			turn := c.currentTurn
			c.busy = false
			c.mu.Unlock()
			if busy {
				if turn != "" {
					b.emit(map[string]any{"v": 1, "type": "turn.completed", "turn": turn, "outcome": "lost"})
				}
			}
			return
		}
		typ, _ := msg["type"].(string)
		switch typ {
		case "control_request":
			c.handleControlRequest(b, msg)
		case "result":
			sub, _ := msg["subtype"].(string)
			outcome := "completed"
			if sub == "error" {
				outcome = "lost"
			}
			c.mu.Lock()
			turn := c.currentTurn
			c.busy = false
			c.currentTurn = ""
			c.mu.Unlock()
			if turn != "" {
				b.emit(map[string]any{"v": 1, "type": "turn.completed", "turn": turn, "outcome": outcome})
			}
		}
	}
}

func (c *claudeLive) handleControlRequest(b *bridge, msg map[string]any) {
	req, _ := msg["request"].(map[string]any)
	sub, _ := req["subtype"].(string)
	if sub != "can_use_tool" {
		c.mu.Lock()
		turn := c.currentTurn
		c.mu.Unlock()
		code := "unsupported_request"
		message := "client does not support this provider request"
		if sub == "ask_user" || strings.Contains(strings.ToLower(sub), "question") {
			code = "provider_question"
			if q := claudeQuestionText(req); q != "" {
				message = q
			} else {
				message = "provider asked a question that structured mode cannot answer here"
			}
		}
		b.emit(map[string]any{
			"v": 1, "type": "notice", "turn": turn, "level": "warn",
			"code": code, "message": message,
		})
		return
	}
	requestID, _ := req["request_id"].(string)
	if requestID == "" {
		requestID, _ = req["id"].(string)
	}
	tool, _ := req["tool"].(string)
	b.emit(map[string]any{
		"v": 1, "type": "request.opened", "turn": c.currentTurn,
		"request": map[string]any{
			"id": requestID, "kind": "command", "command": tool,
			"cwd": c.worktree, "decisions": []string{"allow", "allow_session", "deny"},
		},
	})
}

func claudeQuestionText(req map[string]any) string {
	if req == nil {
		return ""
	}
	for _, key := range []string{"question", "prompt", "message", "text"} {
		if s, _ := req[key].(string); strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return strings.TrimSpace(fmt.Sprint(req["description"]))
}

func (c *claudeLive) shutdown(b *bridge, reason string, code int) {
	c.mu.Lock()
	cmd := c.cmd
	started := c.started
	done := c.readDone
	waitReader := c.readLoopLive
	childIn := c.childIn
	c.mu.Unlock()
	closeChildStdin(childIn)
	exitCode := reapChild(cmd)
	if exitCode == 0 && code != 0 {
		exitCode = code
	}
	if waitReader && done != nil {
		<-done
	}
	c.mu.Lock()
	c.cmd = nil
	c.rpc = nil
	c.childIn = nil
	c.readLoopLive = false
	c.mu.Unlock()
	if started {
		b.emit(map[string]any{"v": 1, "type": "session.ended", "reason": reason, "exit_code": exitCode})
	}
}
