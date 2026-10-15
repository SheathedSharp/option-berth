package agentstream

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/internal/agentlaunch"
)

type opencodeLive struct {
	worktree   string
	resumeFile string
	lookPath   func(string) (string, error)

	mu               sync.Mutex
	cmd              *exec.Cmd
	childIn          io.WriteCloser
	rpc              *jsonrpcConn
	started          bool
	busy             bool
	turnSeq          int
	currentTurn      string
	sessionID        string
	sessionFile      string
	pendingPromptRPC string
	turnMessageBudget int
	decisionOptions  map[string]map[string]string // berth request id -> decision -> ACP optionId
	permRPC          map[string]string            // rpc id -> berth request id
	readDone         chan struct{}
	readLoopLive     bool
}

func newOpenCodeLive(worktree, resumeFile string, lookPath func(string) (string, error)) *opencodeLive {
	return &opencodeLive{
		worktree:        worktree,
		resumeFile:      resumeFile,
		lookPath:        lookPath,
		decisionOptions: map[string]map[string]string{},
		permRPC:         map[string]string{},
	}
}

func (o *opencodeLive) turnStart(b *bridge, id, text string) error {
	if strings.TrimSpace(text) == "" {
		b.emitError(id, "invalid", "turn text must be non-empty")
		return nil
	}
	o.mu.Lock()
	if o.busy {
		o.mu.Unlock()
		b.emitError(id, "busy", "a turn is already running")
		return nil
	}
	o.mu.Unlock()

	if err := o.ensureSession(b); err != nil {
		b.emitError(id, "unsupported", err.Error())
		return nil
	}

	b.emitAck(id)

	o.mu.Lock()
	o.turnSeq++
	turn := fmt.Sprintf("t%d", o.turnSeq)
	o.currentTurn = turn
	o.busy = true
	o.turnMessageBudget = maxTurnMessageBytes
	o.pendingPromptRPC = id
	sessionID := o.sessionID
	o.mu.Unlock()

	b.emit(map[string]any{"v": 1, "type": "turn.started", "turn": turn, "input": text})

	if err := o.writeRPC(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "session/prompt",
		"params": map[string]any{
			"sessionId": sessionID,
			"prompt":    []map[string]any{{"type": "text", "text": text}},
		},
	}); err != nil {
		o.finishTurn(b, turn, "lost")
	}
	return nil
}

func (o *opencodeLive) turnInterrupt(b *bridge, id string) error {
	o.mu.Lock()
	if !o.busy {
		o.mu.Unlock()
		b.emitError(id, "idle", "no turn to interrupt")
		return nil
	}
	sessionID := o.sessionID
	o.mu.Unlock()
	b.emitAck(id)
	_ = o.writeRPC(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/cancel",
		"params":  map[string]any{"sessionId": sessionID},
	})
	return nil
}

func (o *opencodeLive) requestRespond(b *bridge, id, request, decision string) error {
	if request == "" || decision == "" {
		b.emitError(id, "invalid", "request and decision are required")
		return nil
	}
	o.mu.Lock()
	optionMap := o.decisionOptions[request]
	optionID := optionMap[decision]
	rpcID := ""
	for rid, req := range o.permRPC {
		if req == request {
			rpcID = rid
			break
		}
	}
	o.mu.Unlock()
	if optionID == "" || rpcID == "" {
		b.emitError(id, "invalid", "unknown request")
		return nil
	}
	switch decision {
	case "allow", "allow_session", "deny":
	default:
		b.emitError(id, "invalid", "unknown decision")
		return nil
	}
	b.emitAck(id)
	if err := o.writeRPC(map[string]any{
		"jsonrpc": "2.0",
		"id":      rpcID,
		"result": map[string]any{
			"outcome": map[string]any{
				"type":     "selected",
				"optionId": optionID,
			},
		},
	}); err != nil {
		return nil
	}
	b.emit(map[string]any{"v": 1, "type": "request.closed", "request": request, "decision": decision, "by": "client"})
	return nil
}

func (o *opencodeLive) sessionEnd(b *bridge) {
	o.interruptIfBusy()
	o.shutdown(b, "closed", 0)
}

func (o *opencodeLive) interruptIfBusy() {
	o.mu.Lock()
	busy := o.busy
	sessionID := o.sessionID
	o.mu.Unlock()
	if !busy {
		return
	}
	_ = o.writeRPC(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/cancel",
		"params":  map[string]any{"sessionId": sessionID},
	})
	deadline := time.Now().Add(interruptWait)
	for time.Now().Before(deadline) {
		o.mu.Lock()
		idle := !o.busy
		o.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (o *opencodeLive) ensureSession(b *bridge) error {
	o.mu.Lock()
	if o.started {
		o.mu.Unlock()
		return nil
	}
	o.mu.Unlock()

	executable, err := o.lookPath("opencode")
	if err != nil {
		return fmt.Errorf("opencode is not installed")
	}
	cmd := exec.Command(executable, "acp")
	cmd.Dir = o.worktree
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
		return fmt.Errorf("opencode acp failed to start")
	}

	rpc := newJSONRPCConn(stdout, stdin)
	o.mu.Lock()
	o.cmd = cmd
	o.childIn = stdin
	o.rpc = rpc
	o.readDone = make(chan struct{})
	o.mu.Unlock()

	if err := o.handshake(); err != nil {
		o.shutdown(b, "lost", 1)
		return err
	}

	sessionID, sessionPath, err := o.openSession()
	if err != nil {
		o.shutdown(b, "lost", 1)
		return err
	}

	o.mu.Lock()
	o.readLoopLive = true
	o.mu.Unlock()
	go o.readLoop(b)

	o.mu.Lock()
	o.sessionID = sessionID
	o.sessionFile = sessionPath
	o.started = true
	o.mu.Unlock()

	b.emit(map[string]any{
		"v": 1, "type": "session.started",
		"provider": "opencode", "protocol": "acp", "worktree": o.worktree,
		"provider_session_id": sessionID,
		"session_file":        sessionPath,
		"capabilities":        []string{"interrupt", "approvals"},
	})
	return nil
}

func (o *opencodeLive) handshake() error {
	if err := o.writeRPC(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": 1,
			"clientCapabilities": map[string]any{
				"fs": map[string]any{
					"readTextFile":  true,
					"writeTextFile": true,
				},
			},
			"clientInfo": map[string]any{"name": "oberth", "version": "0"},
		},
	}); err != nil {
		return err
	}
	for {
		msg, err := o.rpc.read()
		if err != nil {
			return err
		}
		if rpcID(msg) == "1" {
			if msg["error"] != nil {
				return fmt.Errorf("opencode initialize failed")
			}
			break
		}
		if o.incomingClientRPC(msg) {
			continue
		}
	}
	return nil
}

func (o *opencodeLive) openSession() (sessionID, sessionPath string, err error) {
	if o.resumeFile != "" {
		ref, err := agentlaunch.InspectResume("opencode", o.resumeFile, o.worktree)
		if err != nil {
			return "", "", err
		}
		if err := o.writeRPC(map[string]any{
			"jsonrpc": "2.0",
			"id":      2,
			"method":  "session/load",
			"params": map[string]any{
				"sessionId":  ref.SessionID,
				"cwd":        o.worktree,
				"mcpServers": []any{},
			},
		}); err != nil {
			return "", "", err
		}
		return o.waitSessionResult("2", o.resumeFile)
	}
	if err := o.writeRPC(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "session/new",
		"params": map[string]any{
			"cwd":        o.worktree,
			"mcpServers": []any{},
		},
	}); err != nil {
		return "", "", err
	}
	return o.waitSessionResult("2", "")
}

func (o *opencodeLive) waitSessionResult(expectID, sessionPath string) (string, string, error) {
	for {
		msg, err := o.rpc.read()
		if err != nil {
			return "", "", err
		}
		if o.incomingClientRPC(msg) {
			continue
		}
		if rpcID(msg) == expectID {
			if msg["error"] != nil {
				return "", "", fmt.Errorf("opencode session setup failed")
			}
			result, _ := msg["result"].(map[string]any)
			id, _ := result["sessionId"].(string)
			if id == "" {
				return "", "", fmt.Errorf("opencode session response missing sessionId")
			}
			return id, sessionPath, nil
		}
	}
}

func (o *opencodeLive) writeRPC(v map[string]any) error {
	o.mu.Lock()
	rpc := o.rpc
	o.mu.Unlock()
	if rpc == nil {
		return fmt.Errorf("opencode stream is not connected")
	}
	return rpc.write(v)
}

func (o *opencodeLive) readLoop(b *bridge) {
	o.mu.Lock()
	rpc := o.rpc
	done := o.readDone
	o.mu.Unlock()
	if done != nil {
		defer close(done)
	}
	if rpc == nil {
		return
	}
	for {
		msg, err := rpc.read()
		if err != nil {
			o.mu.Lock()
			busy := o.busy
			turn := o.currentTurn
			o.mu.Unlock()
			if busy && turn != "" {
				o.finishTurn(b, turn, "lost")
			}
			return
		}
		method, _ := msg["method"].(string)
		if method != "" && msg["id"] != nil {
			o.handleAgentRequest(b, msg)
			continue
		}
		if method != "" {
			o.handleNotification(b, method, msg)
			continue
		}
		if rpcID(msg) != "" {
			o.handleRPCResponse(b, msg)
		}
	}
}

func (o *opencodeLive) handleRPCResponse(b *bridge, msg map[string]any) {
	o.mu.Lock()
	expect := o.pendingPromptRPC
	o.mu.Unlock()
	if expect == "" || rpcID(msg) != expect {
		return
	}
	result, _ := msg["result"].(map[string]any)
	stop, _ := result["stopReason"].(string)
	o.mu.Lock()
	turn := o.currentTurn
	o.mu.Unlock()
	outcome := acpStopReason(stop)
	if turn != "" {
		o.finishTurn(b, turn, outcome)
	}
}

func acpStopReason(stop string) string {
	switch strings.ToLower(stop) {
	case "cancelled":
		return "interrupted"
	case "refusal", "max_tokens", "max_turn_requests":
		return "completed"
	default:
		return "completed"
	}
}

func (o *opencodeLive) finishTurn(b *bridge, turn, outcome string) {
	o.mu.Lock()
	o.busy = false
	o.currentTurn = ""
	o.pendingPromptRPC = ""
	o.turnMessageBudget = maxTurnMessageBytes
	o.mu.Unlock()
	b.emit(map[string]any{"v": 1, "type": "turn.completed", "turn": turn, "outcome": outcome})
}

func (o *opencodeLive) handleNotification(b *bridge, method string, msg map[string]any) {
	if method != "session/update" {
		return
	}
	params, _ := msg["params"].(map[string]any)
	update, _ := params["update"].(map[string]any)
	if update == nil {
		return
	}
	kind, _ := update["sessionUpdate"].(string)
	o.mu.Lock()
	turn := o.currentTurn
	budget := o.turnMessageBudget
	o.mu.Unlock()

	switch kind {
	case "agent_message_chunk":
		content, _ := update["content"].(map[string]any)
		text, _ := content["text"].(string)
		messageID, _ := update["messageId"].(string)
		o.emitItemDelta(b, turn, messageID, "message", text, &budget)
	case "agent_thought_chunk":
		content, _ := update["content"].(map[string]any)
		text, _ := content["text"].(string)
		messageID, _ := update["messageId"].(string)
		o.emitItemDelta(b, turn, messageID, "reasoning", text, &budget)
	case "tool_call":
		toolCall, _ := update["toolCall"].(map[string]any)
		if toolCall == nil {
			toolCall = update
		}
		item := acpToolCallItem(toolCall)
		if item != nil {
			b.emit(map[string]any{"v": 1, "type": "item.started", "turn": turn, "item": item})
		}
	case "tool_call_update":
		toolCall, _ := update["toolCall"].(map[string]any)
		if toolCall == nil {
			toolCall = update
		}
		itemID, _ := toolCall["toolCallId"].(string)
		status, _ := toolCall["status"].(string)
		if status == "completed" || status == "failed" {
			item := acpToolCallItem(toolCall)
			if item != nil {
				b.emit(map[string]any{"v": 1, "type": "item.completed", "turn": turn, "item": item})
			}
		} else if delta, _ := toolCall["content"].(string); delta != "" && itemID != "" {
			clipped, _ := clipString(delta, maxCommandTailBytes)
			o.emitItemDelta(b, turn, itemID, "command", clipped, nil)
		}
	case "plan":
		b.emit(map[string]any{"v": 1, "type": "item.started", "turn": turn, "item": map[string]any{
			"id": "plan", "kind": "plan", "status": "running",
		}})
	}
	o.mu.Lock()
	o.turnMessageBudget = budget
	o.mu.Unlock()
}

func acpToolCallItem(tool map[string]any) map[string]any {
	if tool == nil {
		return nil
	}
	id, _ := tool["toolCallId"].(string)
	if id == "" {
		id, _ = tool["id"].(string)
	}
	if id == "" {
		return nil
	}
	kind := "tool"
	if k, _ := tool["kind"].(string); k == "execute" || k == "command" {
		kind = "command"
	}
	title, _ := tool["title"].(string)
	status, _ := tool["status"].(string)
	item := map[string]any{"id": id, "kind": kind, "status": status}
	if title != "" {
		if kind == "command" {
			item["command"] = title
		} else {
			item["name"] = title
		}
	}
	return item
}

func (o *opencodeLive) handleAgentRequest(b *bridge, msg map[string]any) {
	method, _ := msg["method"].(string)
	switch method {
	case "session/request_permission":
		o.handlePermissionRequest(b, msg)
	default:
		o.respondUnsupported(b, msg)
	}
}

func (o *opencodeLive) handlePermissionRequest(b *bridge, msg map[string]any) {
	params, _ := msg["params"].(map[string]any)
	toolCall, _ := params["toolCall"].(map[string]any)
	options, _ := params["options"].([]any)
	requestID := rpcID(msg)
	if requestID == "" {
		requestID = "perm"
	}
	berthID := "r_" + requestID

	decisions := []string{}
	byDecision := map[string]string{}
	o.mu.Lock()
	turn := o.currentTurn
	o.permRPC[requestID] = berthID
	o.mu.Unlock()

	for _, raw := range options {
		opt, _ := raw.(map[string]any)
		if opt == nil {
			continue
		}
		optionID, _ := opt["optionId"].(string)
		kind, _ := opt["kind"].(string)
		decision := acpPermissionDecision(optionID, kind)
		if decision == "" {
			continue
		}
		byDecision[decision] = optionID
		if !containsString(decisions, decision) {
			decisions = append(decisions, decision)
		}
	}
	o.mu.Lock()
	o.decisionOptions[berthID] = byDecision
	o.mu.Unlock()
	if len(decisions) == 0 {
		decisions = []string{"allow", "deny"}
	}

	command := ""
	if toolCall != nil {
		command, _ = toolCall["title"].(string)
	}
	b.emit(map[string]any{
		"v": 1, "type": "request.opened", "turn": turn,
		"request": map[string]any{
			"id": berthID, "kind": "command",
			"command": strings.TrimSpace(command), "cwd": o.worktree,
			"reason": "provider permission request", "decisions": decisions,
		},
	})
}

func acpPermissionDecision(optionID, kind string) string {
	switch strings.ToLower(kind) {
	case "allow_once":
		return "allow"
	case "allow_always":
		return "allow_session"
	case "reject_once", "reject_always":
		return "deny"
	}
	switch optionID {
	case "allow":
		return "allow"
	case "allow_session":
		return "allow_session"
	case "deny":
		return "deny"
	default:
		return ""
	}
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func (o *opencodeLive) respondUnsupported(b *bridge, msg map[string]any) {
	_ = o.writeRPC(map[string]any{
		"jsonrpc": "2.0",
		"id":      msg["id"],
		"error":   map[string]any{"code": -32600, "message": "client does not support this request"},
	})
	o.mu.Lock()
	turn := o.currentTurn
	o.mu.Unlock()
	b.emit(map[string]any{
		"v": 1, "type": "notice", "turn": turn, "level": "warn",
		"code": "unsupported_request", "message": "client does not support this provider request",
	})
}

func (o *opencodeLive) incomingClientRPC(msg map[string]any) bool {
	method, _ := msg["method"].(string)
	if method == "" || msg["id"] == nil {
		return false
	}
	switch method {
	case "fs/read_text_file", "fs/write_text_file":
		o.handleFSRequest(msg)
		return true
	default:
		_ = o.writeRPC(map[string]any{
			"jsonrpc": "2.0",
			"id":      msg["id"],
			"error":   map[string]any{"code": -32600, "message": "client does not support this request"},
		})
		return true
	}
}

func (o *opencodeLive) handleFSRequest(msg map[string]any) {
	method, _ := msg["method"].(string)
	params, _ := msg["params"].(map[string]any)
	path, _ := params["path"].(string)
	abs, err := pathWithinWorktree(o.worktree, path)
	if err != nil {
		_ = o.writeRPC(map[string]any{
			"jsonrpc": "2.0", "id": msg["id"],
			"error": map[string]any{"code": -32000, "message": err.Error()},
		})
		return
	}
	switch method {
	case "fs/read_text_file":
		data, err := os.ReadFile(abs)
		if err != nil {
			_ = o.writeRPC(map[string]any{
				"jsonrpc": "2.0", "id": msg["id"],
				"error": map[string]any{"code": -32000, "message": err.Error()},
			})
			return
		}
		_ = o.writeRPC(map[string]any{
			"jsonrpc": "2.0", "id": msg["id"],
			"result": map[string]any{"content": string(data)},
		})
	case "fs/write_text_file":
		content, _ := params["content"].(string)
		if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
			_ = o.writeRPC(map[string]any{
				"jsonrpc": "2.0", "id": msg["id"],
				"error": map[string]any{"code": -32000, "message": err.Error()},
			})
			return
		}
		_ = o.writeRPC(map[string]any{
			"jsonrpc": "2.0", "id": msg["id"],
			"result": map[string]any{},
		})
	}
}

func pathWithinWorktree(worktree, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path outside worktree")
	}
	return abs, nil
}

func (o *opencodeLive) emitItemDelta(b *bridge, turn, itemID, kind, delta string, budget *int) {
	if turn == "" || itemID == "" || delta == "" {
		return
	}
	clipped, _ := clipWithBudget(delta, maxTurnMessageBytes, budget)
	if clipped == "" && budget != nil {
		return
	}
	b.emit(map[string]any{
		"v": 1, "type": "item.updated", "turn": turn,
		"item": itemID, "kind": kind, "text_delta": clipped,
	})
}

func (o *opencodeLive) shutdown(b *bridge, reason string, code int) {
	o.mu.Lock()
	cmd := o.cmd
	started := o.started
	done := o.readDone
	waitReader := o.readLoopLive
	o.mu.Unlock()

	o.mu.Lock()
	childIn := o.childIn
	o.mu.Unlock()
	closeChildStdin(childIn)
	exitCode := reapChild(cmd)
	if exitCode == 0 && code != 0 {
		exitCode = code
	}
	if waitReader && done != nil {
		<-done
	}
	o.mu.Lock()
	o.cmd = nil
	o.rpc = nil
	o.readLoopLive = false
	o.mu.Unlock()
	if started {
		b.emit(map[string]any{"v": 1, "type": "session.ended", "reason": reason, "exit_code": exitCode})
	}
}
