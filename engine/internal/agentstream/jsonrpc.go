package agentstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

type jsonrpcConn struct {
	mu sync.Mutex
	w  io.Writer
	br *bufio.Reader
}

func newJSONRPCConn(r io.Reader, w io.Writer) *jsonrpcConn {
	return &jsonrpcConn{w: w, br: bufio.NewReader(r)}
}

func (c *jsonrpcConn) write(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.w.Write(raw); err != nil {
		return err
	}
	_, err = c.w.Write([]byte{'\n'})
	return err
}

func (c *jsonrpcConn) read() (map[string]any, error) {
	for {
		line, err := c.br.ReadBytes('\n')
		if len(line) == 0 && err == io.EOF {
			return nil, io.EOF
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			if err == io.EOF {
				return nil, io.EOF
			}
			continue
		}
		if len(trimmed) > maxLineBytes {
			return nil, fmt.Errorf("jsonrpc line exceeds %d bytes", maxLineBytes)
		}
		var value map[string]any
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return nil, fmt.Errorf("invalid jsonrpc json: %w", err)
		}
		return value, nil
	}
}

func rpcID(msg map[string]any) string {
	id := msg["id"]
	if id == nil {
		return ""
	}
	switch v := id.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%.0f", v)
	case int:
		return fmt.Sprintf("%d", v)
	case int64:
		return fmt.Sprintf("%d", v)
	default:
		return fmt.Sprintf("%v", id)
	}
}
