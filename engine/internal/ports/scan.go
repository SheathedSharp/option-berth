package ports

import (
	"bytes"
	"context"
	"errors"
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
	rows, _, err := scanContextSource(ctx)
	return rows, err
}

// scanContextSource also reports which collector answered when it was not the
// platform's primary one. The rows are the same facts either way; the source is
// for the reader deciding how far to trust a port being absent.
func scanContextSource(ctx context.Context) ([]ListeningPort, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	rows, source, err := collectListenersSource(runtime.GOOS, func(name string, args ...string) ([]byte, []byte, error) {
		return executeListenerCommandContext(ctx, name, args...)
	})
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if err != nil {
		if partial, ok := PartialRows(err); ok {
			return mergeAddresses(partial), source, err
		}
		return nil, source, err
	}
	return mergeAddresses(rows), source, nil
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

// scanPlatform is the direct-read entry the CLI seams call.
func scanPlatform() ([]ListeningPort, error) {
	return collectListeners(runtime.GOOS, executeListenerCommand)
}

// listenerSourceLsofName names the Darwin fallback in a published diagnostic:
// its table is narrower than netstat's (see docs/cli.md), so a reader deciding
// whether an absent port means an idle machine needs to know which collector
// answered. The primary collector of every platform stays unnamed.
const listenerSourceLsofName = "lsof"

// collectListeners never substitutes an empty healthy scan for a missing
// executable or failed collector. Darwin uses the TCP PCB table rather than a
// filesystem walk. Diagnostics make the observation incomplete: callers deciding
// that a listener is absent must not receive a healthy output prefix.
func collectListeners(platform string, run listenerCommand) ([]ListeningPort, error) {
	rows, _, err := collectListenersSource(platform, run)
	return rows, err
}

// collectListenersSource also reports which command produced the table: empty
// for the platform's primary collector, listenerSourceLsofName for the Darwin
// fallback. A Darwin process whose parent is not Apple-signed runs netstat
// successfully and still gets no TCP table (issue #156); the PCB sysctl behind
// it is denied while the command itself is not. lsof reads the same sockets by
// walking the file table, which that denial does not cover.
func collectListenersSource(platform string, run listenerCommand) ([]ListeningPort, string, error) {
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
		return nil, "", fmt.Errorf("unsupported platform: %s", platform)
	}
	stdout, stderr, err := run(name, args...)
	detail := strings.TrimSpace(string(stderr))

	if err == nil {
		// Every collector here prints at least a header, even when nothing is
		// listening. A sandbox can let the command exit 0 while denying the
		// syscall behind the table, which leaves stdout empty — that is an
		// observation that never happened, not an empty machine.
		if len(bytes.TrimSpace(stdout)) == 0 {
			primary := observationf(CodeObservationEmpty, observationEmptyHint,
				"%s: exited 0 but printed no listening table%s", name, observationDetailSuffix(detail))
			if platform != "darwin" {
				return nil, "", primary
			}
			// Only the "ran, but the table is empty" failure gets a second
			// collector. A table that was printed and could not be decoded is a
			// decoder problem, and switching tools would hide it.
			return collectDarwinLsof(run, primary)
		}
		if detail != "" {
			return nil, "", observationf(CodeObservationIncomplete, observationIncompleteHint,
				"%s: incomplete listener observation: %s", name, firstObservationLine(detail))
		}
		if format == darwinNetstatListeners {
			rows, decodeErr := decodeDarwinNetstat(string(stdout))
			return rows, "", decodeErr
		}
		return decodeListeners(string(stdout), format), "", nil
	}

	// A non-zero exit is a failed command. With diagnostics it is an
	// observation the caller can classify; with no output at all there is
	// nothing to explain and it stays an ordinary error.
	if detail == "" {
		detail = strings.TrimSpace(string(stdout))
	}
	if detail == "" {
		return nil, "", fmt.Errorf("%s: %w", name, err)
	}
	return nil, "", observationErr(err, CodeObservationIncomplete, observationIncompleteHint,
		"%s: incomplete listener observation: %v\n%s", name, err, firstObservationLine(detail))
}

// collectDarwinLsof runs the fallback and keeps the primary failure when both
// collectors come up empty: the reader needs the reason the first one gave,
// with the second one's in the same sentence, not a bare "no table".
func collectDarwinLsof(run listenerCommand, primary error) ([]ListeningPort, string, error) {
	args := []string{"-a", "-n", "-P", "-w", "+c", "0", "-iTCP", "-sTCP:LISTEN", "-Fpcltn"}
	stdout, stderr, err := run("lsof", args...)
	// lsof exits 1 when nothing matches, so a silent non-zero exit with no
	// output is an empty table rather than a broken command.
	var exited interface{ ExitCode() int }
	emptyMatch := errors.As(err, &exited) && exited.ExitCode() == 1 &&
		len(bytes.TrimSpace(stdout)) == 0 && len(bytes.TrimSpace(stderr)) == 0
	switch {
	case err == nil, emptyMatch:
		rows, decodeErr := decodeDarwinLsof(string(stdout))
		if decodeErr == nil {
			// Two collectors ran and neither printed a row. That is still not
			// proof of an idle machine — it is the same suspicious silence the
			// primary reported — so the self-probe stays the one that decides.
			// Returning an empty table here would let a narrow fallback turn
			// "cannot look" into "nothing is listening".
			if len(rows) == 0 {
				return nil, "", simultaneousFailure(primary,
					fmt.Errorf("lsof: printed no listening table (exit %s)", lsofExit(err)))
			}
			return rows, listenerSourceLsofName, nil
		}
		return nil, "", simultaneousFailure(primary, decodeErr)
	case len(bytes.TrimSpace(stderr)) > 0:
		return nil, "", simultaneousFailure(primary,
			observationErr(err, CodeObservationIncomplete, observationIncompleteHint,
				"lsof: incomplete listener observation: %v\n%s", err, firstObservationLine(strings.TrimSpace(string(stderr)))))
	default:
		return nil, "", simultaneousFailure(primary, fmt.Errorf("lsof: %w", err))
	}
}

// lsofExit names how the fallback ended, for the sentence that reports both
// collectors. A nil error is a clean exit; anything else keeps its own message.
func lsofExit(err error) string {
	if err == nil {
		return "0"
	}
	return err.Error()
}

// simultaneousFailure reports both collectors. The primary keeps its code, so
// a sandbox that denied netstat is still classified as such; the fallback's
// reason goes into the same sentence because "netstat saw nothing and lsof
// could not be used" is one fact about this process, not two.
func simultaneousFailure(primary error, fallback error) error {
	var oe *ObservationError
	if errors.As(primary, &oe) {
		return &ObservationError{
			Code:   oe.Code,
			Detail: primary.Error() + "; fallback: " + fallback.Error(),
			Hint:   oe.Hint,
			Err:    primary,
		}
	}
	return fmt.Errorf("%w; fallback: %w", primary, fallback)
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
