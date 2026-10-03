package ports

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"strings"
)

// Scan collects TCP listening evidence. OS command execution is a thin adapter;
// decoding, socket identity and process-level address folding are pure steps.
func Scan() ([]ListeningPort, error) {
	return ScanContext(context.Background())
}

// ScanContext retains Scan's command ceiling and decoding rules. Cancelled
// command output cannot become a healthy (possibly partial) observation.
func ScanContext(ctx context.Context) ([]ListeningPort, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := collectListeners(runtime.GOOS, func(name string, args ...string) ([]byte, []byte, error) {
		return executeListenerCommandContext(ctx, name, args...)
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return mergeAddresses(rows), nil
}

type listenerCommand func(string, ...string) (stdout, stderr []byte, err error)

func executeListenerCommand(name string, args ...string) ([]byte, []byte, error) {
	return executeListenerCommandContext(context.Background(), name, args...)
}

func executeListenerCommandContext(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd, stop := boundedContext(ctx, name, args...)
	defer stop()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return out, stderr.Bytes(), err
}

func scanPlatform() ([]ListeningPort, error) {
	return collectListeners(runtime.GOOS, executeListenerCommand)
}

// collectListeners never substitutes an empty healthy scan for a missing
// executable or failed collector. Darwin uses the TCP PCB table rather than a
// filesystem walk. Diagnostics make the observation incomplete: callers deciding
// that a listener is absent must not receive a healthy output prefix.
func collectListeners(platform string, run listenerCommand) ([]ListeningPort, error) {
	var name string
	var args []string
	var format listenerFormat
	switch platform {
	case "darwin":
		name, args, format = "netstat", []string{"-anv", "-p", "tcp"}, darwinNetstatListeners
	case "linux":
		name, args, format = "ss", []string{"-tlnp"}, ssListeners
	case "windows":
		name, args, format = "netstat", []string{"-ano"}, netstatListeners
	default:
		return nil, fmt.Errorf("unsupported platform: %s", platform)
	}
	stdout, stderr, err := run(name, args...)
	rows := decodeListeners(string(stdout), format)
	if detail := strings.TrimSpace(string(stderr)); detail != "" {
		if err != nil {
			return nil, fmt.Errorf("%s: incomplete listener observation: %w\n%s", name, err, detail)
		}
		return nil, fmt.Errorf("%s: incomplete listener observation: %s", name, detail)
	}
	if err == nil {
		if format == darwinNetstatListeners {
			return decodeDarwinNetstat(string(stdout))
		}
		return rows, nil
	}

	detail := strings.TrimSpace(string(stderr))
	if detail == "" {
		detail = strings.TrimSpace(string(stdout))
	}
	if detail == "" {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return nil, fmt.Errorf("%s: %w\n%s", name, err, detail)
}
