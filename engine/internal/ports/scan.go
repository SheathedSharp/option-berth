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
	return scanContext(ctx)
}

// scanContext is the one collection path. The self-probe uses it so it tests
// exactly what a real read does; it must not call the probe itself, or a blind
// process would recurse instead of reporting the blind spot.
func scanContext(ctx context.Context) ([]ListeningPort, error) {
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
		if partial, ok := PartialRows(err); ok {
			return mergeAddresses(partial), err
		}
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
		// -l is required: Darwin otherwise truncates numeric IPv6 addresses
		// to 16 characters, even with -n/-v/-W. Never parse a shortened identity.
		name, args, format = "netstat", []string{"-anvl", "-p", "tcp"}, darwinNetstatListeners
	case "linux":
		name, args, format = "ss", []string{"-tlnp"}, ssListeners
	case "windows":
		name, args, format = "netstat", []string{"-ano"}, netstatListeners
	default:
		return nil, fmt.Errorf("unsupported platform: %s", platform)
	}
	stdout, stderr, err := run(name, args...)
	detail := strings.TrimSpace(string(stderr))

	if err == nil {
		// Every collector here prints at least a header, even when nothing is
		// listening. A sandbox can let the command exit 0 while denying the
		// syscall behind the table, which leaves stdout empty — that is an
		// observation that never happened, not an empty machine.
		if len(bytes.TrimSpace(stdout)) == 0 {
			return nil, observationf(CodeObservationEmpty, observationEmptyHint,
				"%s: exited 0 but printed no listening table%s", name, observationDetailSuffix(detail))
		}
		if detail != "" {
			return nil, observationf(CodeObservationIncomplete, observationIncompleteHint,
				"%s: incomplete listener observation: %s", name, firstObservationLine(detail))
		}
		if format == darwinNetstatListeners {
			return decodeDarwinNetstat(string(stdout))
		}
		return decodeListeners(string(stdout), format), nil
	}

	// A non-zero exit is a failed command. With diagnostics it is an
	// observation the caller can classify; with no output at all there is
	// nothing to explain and it stays an ordinary error.
	if detail == "" {
		detail = strings.TrimSpace(string(stdout))
	}
	if detail == "" {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return nil, observationErr(err, CodeObservationIncomplete, observationIncompleteHint,
		"%s: incomplete listener observation: %v\n%s", name, err, firstObservationLine(detail))
}

// observationDetailSuffix folds a command's own diagnostic into a one-line
// observation error without letting a multi-line banner hide the code.
func observationDetailSuffix(detail string) string {
	if detail == "" {
		return ""
	}
	return ": " + firstObservationLine(detail)
}

func firstObservationLine(detail string) string {
	line, _, _ := strings.Cut(detail, "\n")
	return line
}
