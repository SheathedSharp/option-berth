// Fake `codex app-server` for agentstream integration tests.
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
	if len(os.Args) >= 2 && os.Args[1] == "app-server" {
		if err := runAppServer(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}
	fmt.Fprintln(os.Stderr, "usage: fakecodex app-server")
	os.Exit(2)
}

func runAppServer(in io.Reader, out io.Writer) error {
	br := bufio.NewReader(in)
	threadID := "fake-thread-1"
	sessionPath := "/tmp/fake-session.jsonl"
	turnSeq := 0
	holding := false
	var holdTurn string

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
		if method == "" && id != nil && msg["result"] != nil {
			if holding && holdTurn != "" {
				turn := holdTurn
				holding = false
				holdTurn = ""
				if err := write(map[string]any{
					"jsonrpc": "2.0", "method": "turn/completed",
					"params": map[string]any{"turnId": turn, "outcome": "completed"},
				}); err != nil {
					return err
				}
			}
			continue
		}
		switch method {
		case "initialize":
			if err := write(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]any{"userAgent": "fake-codex"},
			}); err != nil {
				return err
			}
		case "initialized":
			continue
		case "thread/start", "thread/resume":
			cwd, _ := msg["params"].(map[string]any)
			if cwd != nil {
				if path, ok := cwd["cwd"].(string); ok && path != "" {
					sessionPath = path + "/session.jsonl"
				}
			}
			thread := map[string]any{"id": threadID, "path": sessionPath, "cwd": "/workspace"}
			if err := write(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"result":  map[string]any{"thread": thread},
			}); err != nil {
				return err
			}
			_ = write(map[string]any{"jsonrpc": "2.0", "method": "thread/started", "params": map[string]any{"thread": thread}})
		case "turn/start":
			if holding {
				continue
			}
			params, _ := msg["params"].(map[string]any)
			text := turnText(params)
			turnSeq++
			turnID := fmt.Sprintf("t%d", turnSeq)
			if text == "unknown" {
				if err := write(map[string]any{
					"jsonrpc": "2.0", "id": 200, "method": "item/permissions/unknownRequest",
					"params": map[string]any{"reason": "unsupported"},
				}); err != nil {
					return err
				}
				continue
			}
			if text == "items" {
				if err := write(map[string]any{
					"jsonrpc": "2.0", "method": "item/agentMessage/delta",
					"params": map[string]any{"turnId": turnID, "itemId": "i1", "delta": "hello"},
				}); err != nil {
					return err
				}
			}
			if text == "hold" || text == "approve" {
				holding = true
				holdTurn = turnID
			}
			if text == "approve" {
				reqID := "r1"
				if err := write(map[string]any{
					"jsonrpc": "2.0", "id": 100, "method": "item/commandExecution/requestApproval",
					"params": map[string]any{
						"requestId": reqID, "command": "rm -rf .cache", "reason": "cleanup",
					},
				}); err != nil {
					return err
				}
				continue
			}
			if text == "hold" {
				continue
			}
			if err := write(map[string]any{
				"jsonrpc": "2.0", "method": "turn/completed",
				"params": map[string]any{"turnId": turnID, "outcome": "completed"},
			}); err != nil {
				return err
			}
		case "turn/interrupt":
			if !holding {
				continue
			}
			turn := holdTurn
			holding = false
			holdTurn = ""
			if err := write(map[string]any{
				"jsonrpc": "2.0", "method": "turn/completed",
				"params": map[string]any{"turnId": turn, "outcome": "interrupted"},
			}); err != nil {
				return err
			}
		}
	}
}

func turnText(params map[string]any) string {
	input, _ := params["input"].([]any)
	for _, item := range input {
		block, _ := item.(map[string]any)
		if block == nil {
			continue
		}
		if text, _ := block["text"].(string); text != "" {
			return text
		}
	}
	return ""
}
