// Fake `claude -p ... stream-json` for agentstream integration tests.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
)

func main() {
	if !streamMode(os.Args[1:]) {
		fmt.Fprintln(os.Stderr, "usage: fakeclaude -p --input-format stream-json --output-format stream-json ...")
		os.Exit(2)
	}
	if err := runStream(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func streamMode(args []string) bool {
	return slices.Contains(args, "-p") && slices.Contains(args, "stream-json")
}

func runStream(in io.Reader, out io.Writer) error {
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
	if err := write(map[string]any{
		"type": "system", "subtype": "init",
		"capabilities": map[string]any{"control": true},
	}); err != nil {
		return err
	}
	br := bufio.NewReader(in)
	holding := false
	var holdTurn int
	turn := 0
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
		typ, _ := msg["type"].(string)
		switch typ {
		case "user":
			text := userText(msg)
			turn++
			if text == "hold" {
				holding = true
				holdTurn = turn
				continue
			}
			if text == "approve" {
				holding = true
				holdTurn = turn
				if err := write(map[string]any{
					"type": "control_request",
					"request": map[string]any{
						"subtype": "can_use_tool", "request_id": "r1", "tool": "rm -rf .cache",
					},
				}); err != nil {
					return err
				}
				continue
			}
			if text == "ask-me" {
				if err := write(map[string]any{
					"type": "control_request",
					"request": map[string]any{
						"subtype": "ask_user", "question": "Which module should we refactor first?",
					},
				}); err != nil {
					return err
				}
				if err := write(map[string]any{"type": "result", "subtype": "success"}); err != nil {
					return err
				}
				continue
			}
			if err := write(map[string]any{"type": "result", "subtype": "success"}); err != nil {
				return err
			}
		case "control_request":
			req, _ := msg["request"].(map[string]any)
			if req != nil && req["subtype"] == "interrupt" && holding {
				holding = false
				if err := write(map[string]any{"type": "result", "subtype": "success"}); err != nil {
					return err
				}
				_ = holdTurn
			}
		case "control_response":
			if holding {
				holding = false
				if err := write(map[string]any{"type": "result", "subtype": "success"}); err != nil {
					return err
				}
			}
		}
	}
}

func userText(msg map[string]any) string {
	message, _ := msg["message"].(map[string]any)
	content, _ := message["content"].([]any)
	for _, block := range content {
		part, _ := block.(map[string]any)
		if text, _ := part["text"].(string); text != "" {
			return text
		}
	}
	return ""
}
