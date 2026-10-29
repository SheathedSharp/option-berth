package agentstream

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/agentlaunch"
)

type codexLive struct {
	worktree   string
	resumeFile string
	lookPath   func(string) (string, error)

	mu            sync.Mutex
	cmd           *exec.Cmd
	childIn       io.WriteCloser
	rpc           *jsonrpcConn
	threadID      string
	sessionFile   string
	started       bool
	busy          bool
	turnSeq       int
	currentTurn   string
	holdTurn      string
	pendingRPC    map[string]string // rpc id -> normalized request id
	requestToRPC  map[string]string
	readDone      chan struct{}
	readLoopLive     bool
	readErr          error
	turnMessageBudget int
}

func newCodexLive(worktree, resumeFile string, lookPath func(string) (string, error)) *codexLive {
	return &codexLive{
		worktree:     worktree,
		resumeFile:   resumeFile,
		lookPath:     lookPath,
		pendingRPC:   map[string]string{},
		requestToRPC: map[string]string{},
	}
}

func (c *codexLive) turnStart(b *bridge, id, text string) error {
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
	c.turnMessageBudget = maxTurnMessageBytes
	c.mu.Unlock()

	b.emit(map[string]any{"v": 1, "type": "turn.started", "turn": turn, "input": text})

	if err := c.writeRPC(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "turn/start",
		"params": map[string]any{
			"threadId": c.threadID,
			"input":    []map[string]any{{"type": "text", "text": text}},
		},
	}); err != nil {
		c.mu.Lock()
		c.busy = false
		c.mu.Unlock()
		b.emit(map[string]any{"v": 1, "type": "turn.completed", "turn": turn, "outcome": "lost"})
		return nil
	}
	if text == "hold" || text == "approve" {
		c.mu.Lock()
		c.holdTurn = turn
		c.mu.Unlock()
	}
	return nil
}

func (c *codexLive) turnInterrupt(b *bridge, id string) error {
	c.mu.Lock()
	if !c.busy {
		c.mu.Unlock()
		b.emitError(id, "idle", "no turn to interrupt")
		return nil
	}
	c.mu.Unlock()
	b.emitAck(id)
	if c.rpc == nil {
		return nil
	}
	_ = c.writeRPC(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "turn/interrupt",
		"params":  map[string]any{"threadId": c.threadID},
	})
	return nil
}

func (c *codexLive) requestRespond(b *bridge, id, request, decision string) error {
	if request == "" || decision == "" {
		b.emitError(id, "invalid", "request and decision are required")
		return nil
	}
	var codexDecision string
	switch decision {
	case "allow":
		codexDecision = "accept"
	case "allow_session":
		codexDecision = "acceptForSession"
	case "deny":
		codexDecision = "decline"
	default:
		b.emitError(id, "invalid", "unknown decision")
		return nil
	}
	c.mu.Lock()
	rpcID := c.requestToRPC[request]
	c.mu.Unlock()
	if rpcID == "" {
		b.emitError(id, "invalid", "unknown request")
		return nil
	}
	b.emitAck(id)
	if err := c.writeRPC(map[string]any{
		"jsonrpc": "2.0",
		"id":      rpcID,
		"result":  map[string]any{"decision": codexDecision},
	}); err != nil {
		return nil
	}
	b.emit(map[string]any{"v": 1, "type": "request.closed", "request": request, "decision": decision, "by": "client"})
	return nil
}

func (c *codexLive) sessionEnd(b *bridge) {
	c.interruptIfBusy()
	c.shutdown(b, "closed", 0)
}

func (c *codexLive) interruptIfBusy() {
	c.mu.Lock()
	busy := c.busy
	threadID := c.threadID
	c.mu.Unlock()
	if !busy {
		return
	}
	_ = c.writeRPC(map[string]any{
		"jsonrpc": "2.0", "id": "interrupt-on-close", "method": "turn/interrupt",
		"params": map[string]any{"threadId": threadID},
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

func (c *codexLive) ensureSession(b *bridge) error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	executable, err := c.lookPath("codex")
	if err != nil {
		return fmt.Errorf("codex is not installed")
	}
	cmd := exec.Command(executable, "app-server")
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
		return fmt.Errorf("codex app-server failed to start")
	}

	rpc := newJSONRPCConn(stdout, stdin)
	c.mu.Lock()
	c.cmd = cmd
	c.childIn = stdin
	c.rpc = rpc
	c.readDone = make(chan struct{})
	c.mu.Unlock()

	if err := c.handshake(); err != nil {
		c.shutdown(b, "lost", 1)
		return err
	}

	thread, path, err := c.openThread()
	if err != nil {
		c.shutdown(b, "lost", 1)
		return err
	}

	c.mu.Lock()
	c.readLoopLive = true
	c.mu.Unlock()
	go c.readLoop(b)

	c.mu.Lock()
	c.threadID = thread
	c.sessionFile = path
	c.started = true
	c.mu.Unlock()

	b.emit(map[string]any{
		"v": 1, "type": "session.started",
		"provider": "codex", "protocol": "codex-app-server", "worktree": c.worktree,
		"provider_session_id": thread,
		"session_file":        path,
		"capabilities":        []string{"interrupt", "approvals", "turn_diff"},
	})
	return nil
}

func (c *codexLive) writeRPC(v map[string]any) error {
	c.mu.Lock()
	rpc := c.rpc
	c.mu.Unlock()
	if rpc == nil {
		return fmt.Errorf("codex stream is not connected")
	}
	return rpc.write(v)
}

func (c *codexLive) handshake() error {
	if err := c.writeRPC(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"clientInfo":   map[string]any{"name": "oberth", "version": "0"},
			"capabilities": map[string]any{},
		},
	}); err != nil {
		return err
	}
	for {
		msg, err := c.rpc.read()
		if err != nil {
			return err
		}
		if rpcID(msg) == "1" {
			if msg["error"] != nil {
				return fmt.Errorf("codex initialize failed")
			}
			break
		}
	}
	return c.writeRPC(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
}

func (c *codexLive) openThread() (threadID, sessionPath string, err error) {
	if c.resumeFile != "" {
		ref, err := agentlaunch.InspectResume("codex", c.resumeFile, c.worktree)
		if err != nil {
			return "", "", err
		}
		if err := c.writeRPC(map[string]any{
			"jsonrpc": "2.0",
			"id":      2,
			"method":  "thread/resume",
			"params": map[string]any{
				"threadId":     ref.SessionID,
				"excludeTurns": true,
			},
		}); err != nil {
			return "", "", err
		}
		return c.waitThreadResult("2")
	}
	if err := c.writeRPC(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "thread/start",
		"params":  map[string]any{"cwd": c.worktree},
	}); err != nil {
		return "", "", err
	}
	return c.waitThreadResult("2")
}

func (c *codexLive) waitThreadResult(expectID string) (string, string, error) {
	for {
		msg, err := c.rpc.read()
		if err != nil {
			return "", "", err
		}
		if rpcID(msg) == expectID {
			if msg["error"] != nil {
				return "", "", fmt.Errorf("codex thread setup failed")
			}
			result, _ := msg["result"].(map[string]any)
			thread, _ := result["thread"].(map[string]any)
			if thread == nil {
				return "", "", fmt.Errorf("codex thread response missing thread")
			}
			id, _ := thread["id"].(string)
			path, _ := thread["path"].(string)
			if id == "" {
				return "", "", fmt.Errorf("codex thread response missing id")
			}
			return id, path, nil
		}
	}
}

func (c *codexLive) readLoop(b *bridge) {
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
			c.readErr = err
			c.mu.Unlock()
			if c.busy {
				turn := c.currentTurn
				c.mu.Lock()
				c.busy = false
				c.mu.Unlock()
				if turn != "" {
					b.emit(map[string]any{"v": 1, "type": "turn.completed", "turn": turn, "outcome": "lost"})
				}
			}
			return
		}
		method, _ := msg["method"].(string)
		if method != "" && msg["id"] != nil {
			c.handleServerRequest(b, msg)
			continue
		}
		if method != "" {
			c.handleNotification(b, method, msg)
		}
	}
}

func (c *codexLive) handleNotification(b *bridge, method string, msg map[string]any) {
	params, _ := msg["params"].(map[string]any)
	c.mu.Lock()
	turn := c.currentTurn
	budget := c.turnMessageBudget
	c.mu.Unlock()
	if turn == "" && params != nil {
		turn, _ = params["turnId"].(string)
	}
	switch method {
	case "item/agentMessage/delta", "item/reasoning/delta":
		delta, _ := params["delta"].(string)
		itemID, _ := params["itemId"].(string)
		if itemID == "" {
			itemID, _ = params["id"].(string)
		}
		kind := "message"
		if method == "item/reasoning/delta" {
			kind = "reasoning"
		}
		c.emitItemUpdated(b, turn, itemID, kind, delta, &budget)
		c.mu.Lock()
		c.turnMessageBudget = budget
		c.mu.Unlock()
	case "item/commandExecution/outputDelta":
		delta, _ := params["delta"].(string)
		itemID, _ := params["itemId"].(string)
		clipped, _ := clipString(delta, maxCommandTailBytes)
		c.emitItemUpdated(b, turn, itemID, "command", clipped, nil)
	case "item/started":
		item := codexStreamItem(params)
		if item != nil {
			b.emit(map[string]any{"v": 1, "type": "item.started", "turn": turn, "item": item})
		}
	case "item/completed":
		item := codexStreamItem(params)
		if item != nil {
			b.emit(map[string]any{"v": 1, "type": "item.completed", "turn": turn, "item": item})
		}
	case "turn/diff/updated":
		patch, _ := params["patch"].(string)
		patch, truncated := clipString(patch, maxTurnDiffBytes)
		files, _ := params["files"].([]any)
		b.emit(map[string]any{
			"v": 1, "type": "turn.diff", "turn": turn,
			"files": files, "patch": patch, "truncated": truncated,
		})
	case "turn/completed":
		outcome := codexTurnOutcome(params)
		c.mu.Lock()
		turn := c.currentTurn
		if turn == "" {
			turn, _ = params["turnId"].(string)
		}
		c.busy = false
		c.holdTurn = ""
		c.currentTurn = ""
		c.turnMessageBudget = maxTurnMessageBytes
		c.mu.Unlock()
		if turn != "" {
			b.emit(map[string]any{"v": 1, "type": "turn.completed", "turn": turn, "outcome": outcome})
		}
	}
}

func codexTurnOutcome(params map[string]any) string {
	if params == nil {
		return "completed"
	}
	if outcome, _ := params["outcome"].(string); outcome != "" {
		return normalizeOutcome(outcome)
	}
	if turn, ok := params["turn"].(map[string]any); ok {
		if status, ok := turn["status"].(map[string]any); ok {
			if typ, _ := status["type"].(string); typ != "" {
				return normalizeOutcome(typ)
			}
		}
	}
	return "completed"
}

func normalizeOutcome(raw string) string {
	switch strings.ToLower(raw) {
	case "interrupted", "cancelled", "canceled":
		return "interrupted"
	case "lost", "failed", "error":
		return "lost"
	default:
		return "completed"
	}
}

func (c *codexLive) handleServerRequest(b *bridge, msg map[string]any) {
	method, _ := msg["method"].(string)
	if !strings.HasSuffix(method, "requestApproval") {
		_ = c.writeRPC(map[string]any{
			"jsonrpc": "2.0",
			"id":      msg["id"],
			"error":   map[string]any{"code": -32600, "message": "client does not support this request"},
		})
		c.mu.Lock()
		turn := c.currentTurn
		c.mu.Unlock()
		b.emit(map[string]any{
			"v": 1, "type": "notice", "turn": turn, "level": "warn",
			"code": "unsupported_request", "message": "client does not support this provider request",
		})
		return
	}
	params, _ := msg["params"].(map[string]any)
	requestID, _ := params["requestId"].(string)
	if requestID == "" {
		requestID, _ = params["id"].(string)
	}
	if requestID == "" {
		requestID = rpcID(msg)
	}
	c.mu.Lock()
	turn := c.currentTurn
	c.requestToRPC[requestID] = rpcID(msg)
	c.pendingRPC[rpcID(msg)] = requestID
	c.mu.Unlock()

	decisions := []string{"allow", "allow_session", "deny"}
	b.emit(map[string]any{
		"v": 1, "type": "request.opened", "turn": turn,
		"request": map[string]any{
			"id": requestID, "kind": "command",
			"command": strings.TrimSpace(fmt.Sprint(params["command"])),
			"cwd":     c.worktree,
			"reason":  strings.TrimSpace(fmt.Sprint(params["reason"])),
			"decisions": decisions,
		},
	})
}

func (c *codexLive) shutdown(b *bridge, reason string, code int) {
	c.mu.Lock()
	cmd := c.cmd
	started := c.started
	done := c.readDone
	waitReader := c.readLoopLive
	c.mu.Unlock()

	c.mu.Lock()
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
	c.readLoopLive = false
	c.mu.Unlock()
	if started {
		b.emit(map[string]any{"v": 1, "type": "session.ended", "reason": reason, "exit_code": exitCode})
	}
}
