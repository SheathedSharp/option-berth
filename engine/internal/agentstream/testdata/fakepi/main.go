// Fake `pi --mode rpc` for agentstream integration tests.
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
	if len(os.Args) >= 3 && os.Args[1] == "--mode" && os.Args[2] == "rpc" {
		if err := runRPC(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}
	fmt.Fprintln(os.Stderr, "usage: fakepi --mode rpc")
	os.Exit(2)
}

func runRPC(in io.Reader, out io.Writer) error {
	br := bufio.NewReader(in)
	busy := false
	var pendingUI string
	var pendingPrompt any

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

	emitTurn := func(text string) error {
		if err := write(map[string]any{"type": "agent_start"}); err != nil {
			return err
		}
		if err := write(map[string]any{"type": "turn_start"}); err != nil {
			return err
		}
		if err := write(map[string]any{
			"type": "message_update",
			"assistantMessageEvent": map[string]any{
				"type": "text_delta", "contentIndex": 0, "delta": "echo:" + text,
			},
		}); err != nil {
			return err
		}
		return write(map[string]any{"type": "agent_settled", "aborted": false})
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
			return write(map[string]any{
				"type": "response", "command": "parse", "success": false,
				"error": "Failed to parse command",
			})
		}
		typ, _ := msg["type"].(string)
		id := msg["id"]
		switch typ {
		case "get_state":
			if err := write(map[string]any{
				"id": id, "type": "response", "command": "get_state", "success": true,
				"data": map[string]any{
					"sessionId": "fake-pi-session", "sessionFile": "/tmp/fake-pi.jsonl",
					"isStreaming": busy,
				},
			}); err != nil {
				return err
			}
		case "prompt":
			if busy {
				_ = write(map[string]any{
					"id": id, "type": "response", "command": "prompt", "success": false,
					"error": "agent is streaming",
				})
				continue
			}
			text, _ := msg["message"].(string)
			busy = true
			if err := write(map[string]any{
				"id": id, "type": "response", "command": "prompt", "success": true,
				"data": map[string]any{"disposition": "started"},
			}); err != nil {
				return err
			}
			switch text {
			case "hold", "approve":
				pendingPrompt = id
				if text == "approve" {
					pendingUI = "ui-1"
					if err := write(map[string]any{
						"type": "extension_ui_request", "id": pendingUI, "method": "select",
						"title": "Allow dangerous command?", "options": []string{"Allow", "Allow for session", "Deny"},
					}); err != nil {
						return err
					}
				}
				continue
			default:
				if err := emitTurn(text); err != nil {
					return err
				}
				busy = false
			}
		case "abort":
			if pendingPrompt != nil {
				_ = write(map[string]any{"type": "agent_settled", "aborted": true})
				pendingPrompt = nil
				pendingUI = ""
				busy = false
			}
			_ = write(map[string]any{
				"id": id, "type": "response", "command": "abort", "success": true,
			})
		case "extension_ui_response":
			if pendingUI != "" && fmt.Sprint(msg["id"]) == pendingUI {
				pendingUI = ""
				if pendingPrompt != nil {
					if err := emitTurn("approved"); err != nil {
						return err
					}
					pendingPrompt = nil
					busy = false
				}
			}
		default:
			if id != nil {
				_ = write(map[string]any{
					"id": id, "type": "response", "command": typ, "success": false,
					"error": "not implemented",
				})
			}
		}
	}
}
