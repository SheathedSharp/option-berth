package agentstream

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/internal/agentlaunch"
)

type piLive struct {
	worktree   string
	resumeFile string
	lookPath   func(string) (string, error)

	mu              sync.Mutex
	cmd             *exec.Cmd
	childIn         io.WriteCloser
	br              *bufio.Reader
	started         bool
	busy            bool
	turnSeq         int
	currentTurn     string
	pendingPromptID string
	sessionID       string
	sessionFile     string
	turnMessageBudget int
	uiRequests      map[string]string            // berth request id -> pi ui id
	decisionLabels  map[string]map[string]string // berth request id -> decision -> option label
	readDone        chan struct{}
	readLoopLive    bool
}

func newPiLive(worktree, resumeFile string, lookPath func(string) (string, error)) *piLive {
	return &piLive{
		worktree:       worktree,
		resumeFile:     resumeFile,
		lookPath:       lookPath,
		uiRequests:     map[string]string{},
		decisionLabels: map[string]map[string]string{},
	}
}

func (p *piLive) turnStart(b *bridge, id, text string) error {
	if strings.TrimSpace(text) == "" {
		b.emitError(id, "invalid", "turn text must be non-empty")
		return nil
	}
	p.mu.Lock()
	if p.busy {
		p.mu.Unlock()
		b.emitError(id, "busy", "a turn is already running")
		return nil
	}
	p.mu.Unlock()

	if err := p.ensureSession(b); err != nil {
		b.emitError(id, "unsupported", err.Error())
		return nil
	}

	b.emitAck(id)

	p.mu.Lock()
	p.turnSeq++
	turn := fmt.Sprintf("t%d", p.turnSeq)
	p.currentTurn = turn
	p.busy = true
	p.pendingPromptID = id
	p.turnMessageBudget = maxTurnMessageBytes
	p.mu.Unlock()

	b.emit(map[string]any{"v": 1, "type": "turn.started", "turn": turn, "input": text})

	if err := p.writeCommand(map[string]any{"id": id, "type": "prompt", "message": text}); err != nil {
		p.finishTurn(b, turn, "lost")
	}
	return nil
}

func (p *piLive) turnInterrupt(b *bridge, id string) error {
	p.mu.Lock()
	if !p.busy {
		p.mu.Unlock()
		b.emitError(id, "idle", "no turn to interrupt")
		return nil
	}
	p.mu.Unlock()
	b.emitAck(id)
	if err := p.writeCommand(map[string]any{"id": id, "type": "abort"}); err != nil {
		return nil
	}
	return nil
}

func (p *piLive) requestRespond(b *bridge, id, request, decision string) error {
	if request == "" || decision == "" {
		b.emitError(id, "invalid", "request and decision are required")
		return nil
	}
	p.mu.Lock()
	uiID := p.uiRequests[request]
	labels := p.decisionLabels[request]
	label := labels[decision]
	p.mu.Unlock()
	if uiID == "" || label == "" {
		b.emitError(id, "invalid", "unknown request")
		return nil
	}
	b.emitAck(id)
	if err := p.writeCommand(map[string]any{
		"type": "extension_ui_response", "id": uiID, "value": label,
	}); err != nil {
		return nil
	}
	b.emit(map[string]any{"v": 1, "type": "request.closed", "request": request, "decision": decision, "by": "client"})
	return nil
}

func (p *piLive) sessionEnd(b *bridge) {
	p.interruptIfBusy()
	p.shutdown(b, "closed", 0)
}

func (p *piLive) interruptIfBusy() {
	p.mu.Lock()
	busy := p.busy
	p.mu.Unlock()
	if !busy {
		return
	}
	_ = p.writeCommand(map[string]any{"type": "abort"})
	deadline := time.Now().Add(interruptWait)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		idle := !p.busy
		p.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (p *piLive) ensureSession(b *bridge) error {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()

	executable, err := p.lookPath("pi")
	if err != nil {
		return fmt.Errorf("pi is not installed")
	}
	args := []string{"--mode", "rpc"}
	if p.resumeFile != "" {
		ref, err := agentlaunch.InspectResume("pi", p.resumeFile, p.worktree)
		if err != nil {
			return err
		}
		args = append(args, "--session", ref.Source)
	}
	cmd := exec.Command(executable, args...)
	cmd.Dir = p.worktree
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
		return fmt.Errorf("pi rpc failed to start")
	}

	p.mu.Lock()
	p.cmd = cmd
	p.childIn = stdin
	p.br = bufio.NewReader(stdout)
	p.readDone = make(chan struct{})
	p.mu.Unlock()

	state, err := p.queryState()
	if err != nil {
		p.shutdown(b, "lost", 1)
		return err
	}
	sessionID, _ := state["sessionId"].(string)
	sessionFile, _ := state["sessionFile"].(string)
	if sessionID == "" {
		sessionID = "pi-session"
	}

	p.mu.Lock()
	p.sessionID = sessionID
	p.sessionFile = sessionFile
	p.started = true
	p.readLoopLive = true
	p.mu.Unlock()

	go p.readLoop(b)

	b.emit(map[string]any{
		"v": 1, "type": "session.started",
		"provider": "pi", "protocol": "pi-rpc", "worktree": p.worktree,
		"provider_session_id": sessionID,
		"session_file":        sessionFile,
		"capabilities":        []string{"interrupt"},
	})
	return nil
}

func (p *piLive) queryState() (map[string]any, error) {
	if err := p.writeCommand(map[string]any{"id": "state-1", "type": "get_state"}); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		br := p.br
		p.mu.Unlock()
		if br == nil {
			return nil, fmt.Errorf("pi rpc is not connected")
		}
		line, err := br.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		record, err := parseJSONLine(line)
		if err != nil {
			continue
		}
		if record["type"] == "response" && fmt.Sprint(record["id"]) == "state-1" {
			if record["success"] != true {
				return nil, fmt.Errorf("pi get_state failed")
			}
			data, _ := record["data"].(map[string]any)
			if data == nil {
				return nil, fmt.Errorf("pi get_state missing data")
			}
			return data, nil
		}
		// Hand off stray events to readLoop by re-processing - rare during init; drop for fake
	}
	return nil, fmt.Errorf("pi get_state timed out")
}

func (p *piLive) writeCommand(v map[string]any) error {
	p.mu.Lock()
	in := p.childIn
	p.mu.Unlock()
	if in == nil {
		return fmt.Errorf("pi stream is not connected")
	}
	raw, err := marshalJSONLine(v)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.childIn == nil {
		return fmt.Errorf("pi stream is not connected")
	}
	_, err = p.childIn.Write(raw)
	return err
}

func (p *piLive) readLoop(b *bridge) {
	p.mu.Lock()
	done := p.readDone
	p.mu.Unlock()
	if done != nil {
		defer close(done)
	}
	for {
		p.mu.Lock()
		br := p.br
		p.mu.Unlock()
		if br == nil {
			return
		}
		line, err := br.ReadBytes('\n')
		if err != nil {
			p.mu.Lock()
			busy := p.busy
			turn := p.currentTurn
			p.mu.Unlock()
			if busy && turn != "" {
				p.finishTurn(b, turn, "lost")
			}
			return
		}
		record, err := parseJSONLine(line)
		if err != nil {
			continue
		}
		p.handleRecord(b, record)
	}
}

func (p *piLive) handleRecord(b *bridge, record map[string]any) {
	typ, _ := record["type"].(string)
	switch typ {
	case "response":
		p.handleResponse(b, record)
	case "message_update":
		p.handleMessageUpdate(b, record)
	case "tool_execution_start":
		p.handleToolStart(b, record)
	case "tool_execution_end":
		p.handleToolEnd(b, record)
	case "agent_settled":
		p.handleAgentSettled(b, record)
	case "extension_ui_request":
		p.handleUIRequest(b, record)
	}
}

func (p *piLive) handleResponse(b *bridge, record map[string]any) {
	command, _ := record["command"].(string)
	if command != "prompt" {
		return
	}
	p.mu.Lock()
	expect := p.pendingPromptID
	turn := p.currentTurn
	p.mu.Unlock()
	if expect == "" || fmt.Sprint(record["id"]) != expect {
		return
	}
	if record["success"] != true {
		code := "lost"
		if msg, _ := record["error"].(string); strings.Contains(strings.ToLower(msg), "streaming") {
			code = "busy"
		}
		b.emitError(expect, code, strings.TrimSpace(fmt.Sprint(record["error"])))
		if turn != "" {
			p.finishTurn(b, turn, "lost")
		}
	}
}

func (p *piLive) handleMessageUpdate(b *bridge, record map[string]any) {
	event, _ := record["assistantMessageEvent"].(map[string]any)
	if event == nil {
		return
	}
	eventType, _ := event["type"].(string)
	p.mu.Lock()
	turn := p.currentTurn
	budget := p.turnMessageBudget
	p.mu.Unlock()
	switch eventType {
	case "text_delta":
		delta, _ := event["delta"].(string)
		idx, _ := event["contentIndex"].(float64)
		itemID := fmt.Sprintf("msg-%d", int(idx))
		p.emitItemDelta(b, turn, itemID, "message", delta, &budget)
	case "thinking_delta":
		delta, _ := event["delta"].(string)
		idx, _ := event["contentIndex"].(float64)
		itemID := fmt.Sprintf("think-%d", int(idx))
		p.emitItemDelta(b, turn, itemID, "reasoning", delta, &budget)
	}
	p.mu.Lock()
	p.turnMessageBudget = budget
	p.mu.Unlock()
}

func (p *piLive) handleToolStart(b *bridge, record map[string]any) {
	p.mu.Lock()
	turn := p.currentTurn
	p.mu.Unlock()
	item := piToolItem(record, "running")
	if item != nil {
		b.emit(map[string]any{"v": 1, "type": "item.started", "turn": turn, "item": item})
	}
}

func (p *piLive) handleToolEnd(b *bridge, record map[string]any) {
	p.mu.Lock()
	turn := p.currentTurn
	p.mu.Unlock()
	item := piToolItem(record, "completed")
	if record["isError"] == true {
		item["status"] = "failed"
	}
	if item != nil {
		b.emit(map[string]any{"v": 1, "type": "item.completed", "turn": turn, "item": item})
	}
}

func piToolItem(record map[string]any, status string) map[string]any {
	id, _ := record["toolCallId"].(string)
	name, _ := record["toolName"].(string)
	if id == "" {
		return nil
	}
	kind := "tool"
	if name == "bash" || name == "powershell" {
		kind = "command"
	}
	item := map[string]any{"id": id, "kind": kind, "status": status}
	if kind == "command" {
		args, _ := record["args"].(map[string]any)
		if cmd, _ := args["command"].(string); cmd != "" {
			item["command"] = cmd
		} else {
			item["command"] = name
		}
	} else {
		item["name"] = name
	}
	return item
}

func (p *piLive) handleAgentSettled(b *bridge, record map[string]any) {
	p.mu.Lock()
	if !p.busy {
		p.mu.Unlock()
		return
	}
	turn := p.currentTurn
	p.mu.Unlock()
	outcome := "completed"
	if record["aborted"] == true {
		outcome = "interrupted"
	}
	p.finishTurn(b, turn, outcome)
}

func (p *piLive) handleUIRequest(b *bridge, record map[string]any) {
	method, _ := record["method"].(string)
	if method != "select" && method != "confirm" {
		b.emit(map[string]any{
			"v": 1, "type": "notice", "turn": p.currentTurn, "level": "warn",
			"code": "unsupported_request", "message": "client does not support this provider UI request",
		})
		return
	}
	uiID, _ := record["id"].(string)
	if uiID == "" {
		return
	}
	berthID := "r_" + uiID
	decisions := []string{}
	labels := map[string]string{}
	if method == "confirm" {
		decisions = []string{"allow", "deny"}
		labels["allow"] = "yes"
		labels["deny"] = "no"
	} else {
		options, _ := record["options"].([]any)
		for _, raw := range options {
			opt, _ := raw.(string)
			decision := piOptionDecision(opt)
			if decision == "" {
				continue
			}
			labels[decision] = opt
			if !containsString(decisions, decision) {
				decisions = append(decisions, decision)
			}
		}
	}
	if len(decisions) == 0 {
		decisions = []string{"allow", "deny"}
		labels["allow"] = "Allow"
		labels["deny"] = "Deny"
	}
	p.mu.Lock()
	turn := p.currentTurn
	p.uiRequests[berthID] = uiID
	p.decisionLabels[berthID] = labels
	p.mu.Unlock()
	title, _ := record["title"].(string)
	b.emit(map[string]any{
		"v": 1, "type": "request.opened", "turn": turn,
		"request": map[string]any{
			"id": berthID, "kind": "command",
			"command": strings.TrimSpace(title), "cwd": p.worktree,
			"reason": "provider permission request", "decisions": decisions,
		},
	})
}

func piOptionDecision(label string) string {
	lower := strings.ToLower(strings.TrimSpace(label))
	switch {
	case strings.Contains(lower, "session"):
		return "allow_session"
	case strings.HasPrefix(lower, "allow") || lower == "yes":
		return "allow"
	case strings.HasPrefix(lower, "deny") || strings.HasPrefix(lower, "block") || lower == "no":
		return "deny"
	default:
		return ""
	}
}

func (p *piLive) finishTurn(b *bridge, turn, outcome string) {
	p.mu.Lock()
	p.busy = false
	p.currentTurn = ""
	p.pendingPromptID = ""
	p.turnMessageBudget = maxTurnMessageBytes
	p.mu.Unlock()
	if turn != "" {
		b.emit(map[string]any{"v": 1, "type": "turn.completed", "turn": turn, "outcome": outcome})
	}
}

func (p *piLive) emitItemDelta(b *bridge, turn, itemID, kind, delta string, budget *int) {
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

func (p *piLive) shutdown(b *bridge, reason string, code int) {
	p.mu.Lock()
	cmd := p.cmd
	started := p.started
	done := p.readDone
	waitReader := p.readLoopLive
	p.mu.Unlock()

	p.mu.Lock()
	childIn := p.childIn
	p.mu.Unlock()
	closeChildStdin(childIn)
	exitCode := reapChild(cmd)
	if exitCode == 0 && code != 0 {
		exitCode = code
	}
	if waitReader && done != nil {
		<-done
	}
	p.mu.Lock()
	p.cmd = nil
	p.br = nil
	p.readLoopLive = false
	p.mu.Unlock()
	if started {
		b.emit(map[string]any{"v": 1, "type": "session.ended", "reason": reason, "exit_code": exitCode})
	}
}

func parseJSONLine(line []byte) (map[string]any, error) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return nil, fmt.Errorf("empty line")
	}
	if len(trimmed) > maxLineBytes {
		return nil, fmt.Errorf("line too long")
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return nil, err
	}
	return value, nil
}

func marshalJSONLine(v map[string]any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}
