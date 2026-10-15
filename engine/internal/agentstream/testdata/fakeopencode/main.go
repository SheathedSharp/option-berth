// Fake `opencode acp` for agentstream integration tests.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "acp" {
		if err := runACP(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}
	fmt.Fprintln(os.Stderr, "usage: fakeopencode acp")
	os.Exit(2)
}

func runACP(in io.Reader, out io.Writer) error {
	br := bufio.NewReader(in)
	sessionID := "sess_fake_opencode_1"
	turnSeq := 0
	var pendingPrompt any
	var pendingPerm string

	write := func(v any) error {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := out.Write(raw); err != nil {
			return err
		}
		_, err = out.Write([]byte{'\n'})
		return err
	}

	completePrompt := func(id any, stop string) error {
		pendingPrompt = nil
		return write(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"result": map[string]any{"stopReason": stop},
		})
	}

	for {
		line, err := br.ReadBytes('\n')
		if len(line) == 0 && err == io.EOF {
			return nil
		}
		if err != nil && err != io.EOF {
			return err
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal(trimmed, &msg); err != nil {
			return err
		}
		method, _ := msg["method"].(string)
		id := msg["id"]
		if method == "" && id != nil {
			if pendingPerm != "" && fmt.Sprint(id) == pendingPerm {
				pendingPerm = ""
				if pendingPrompt != nil {
					if err := completePrompt(pendingPrompt, "end_turn"); err != nil {
						return err
					}
				}
				continue
			}
			continue
		}
		switch method {
		case "initialize":
			if err := write(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{
					"protocolVersion": 1,
					"agentCapabilities": map[string]any{
						"loadSession": true,
						"promptCapabilities": map[string]any{},
					},
					"agentInfo": map[string]any{"name": "fake-opencode", "version": "0.0.0"},
				},
			}); err != nil {
				return err
			}
		case "session/new", "session/load":
			if err := write(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result":  map[string]any{"sessionId": sessionID},
			}); err != nil {
				return err
			}
		case "session/prompt":
			turnSeq++
			params, _ := msg["params"].(map[string]any)
			text := promptText(params)
			if err := write(map[string]any{
				"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{
					"sessionId": sessionID,
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"messageId":     fmt.Sprintf("msg_%d", turnSeq),
						"content":       map[string]any{"type": "text", "text": "echo:" + text},
					},
				},
			}); err != nil {
				return err
			}
			switch text {
			case "hold", "approve":
				pendingPrompt = id
				if text == "approve" {
					pendingPerm = "perm_1"
					if err := write(map[string]any{
						"jsonrpc": "2.0", "method": "session/request_permission", "id": pendingPerm,
						"params": map[string]any{
							"sessionId": sessionID,
							"toolCall": map[string]any{
								"toolCallId": "call_1",
								"title":      "rm -rf .cache",
								"kind":       "execute",
								"status":     "pending",
							},
							"options": []map[string]any{
								{"optionId": "allow", "kind": "allow_once", "name": "Allow"},
								{"optionId": "allow_session", "kind": "allow_always", "name": "Allow for session"},
								{"optionId": "deny", "kind": "reject_once", "name": "Deny"},
							},
						},
					}); err != nil {
						return err
					}
				}
				continue
			default:
				if err := completePrompt(id, "end_turn"); err != nil {
					return err
				}
			}
		case "session/cancel":
			if pendingPrompt != nil {
				pid := pendingPrompt
				pendingPrompt = nil
				pendingPerm = ""
				if err := completePrompt(pid, "cancelled"); err != nil {
					return err
				}
			}
		default:
			if id != nil {
				_ = write(map[string]any{
					"jsonrpc": "2.0", "id": id,
					"error":   map[string]any{"code": -32601, "message": "not implemented"},
				})
			}
		}
	}
}

func promptText(params map[string]any) string {
	prompt, _ := params["prompt"].([]any)
	if len(prompt) == 0 {
		return ""
	}
	block, _ := prompt[0].(map[string]any)
	text, _ := block["text"].(string)
	return text
}
