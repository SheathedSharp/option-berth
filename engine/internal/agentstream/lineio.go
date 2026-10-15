package agentstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const maxLineBytes = 2 * 1024 * 1024

type lineWriter struct {
	w   io.Writer
	err error
}

func (lw *lineWriter) emit(v any) {
	if lw.err != nil {
		return
	}
	raw, err := json.Marshal(v)
	if err != nil {
		lw.err = err
		return
	}
	if len(raw) > maxLineBytes {
		lw.err = fmt.Errorf("encoded event exceeds %d bytes", maxLineBytes)
		return
	}
	if _, err := lw.w.Write(raw); err != nil {
		lw.err = err
		return
	}
	_, lw.err = lw.w.Write([]byte{'\n'})
}

func readCommand(br *bufio.Reader) (map[string]any, error) {
	for {
		line, err := br.ReadBytes('\n')
		if len(line) == 0 && err == io.EOF {
			return nil, io.EOF
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		if len(line) > maxLineBytes {
			return nil, fmt.Errorf("command line exceeds %d bytes", maxLineBytes)
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			if err == io.EOF {
				return nil, io.EOF
			}
			continue
		}
		var value map[string]any
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return nil, fmt.Errorf("invalid command json: %w", err)
		}
		return value, nil
	}
}
