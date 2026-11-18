package ports

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/ports/breaker"
)

// Scan collects TCP listening evidence. OS command execution is a thin adapter;
// decoding, socket identity and process-level address folding are pure steps.
func Scan() ([]ListeningPort, error) {
	return ScanContext(context.Background())
}

// ScanContext retains Scan's command ceiling and decoding rules. Cancelled
// command output cannot become a healthy (possibly partial) observation.
func ScanContext(ctx context.Context) ([]ListeningPort, error) {
	read, err := ScanContextRead(ctx, nil)
	return read.Rows, err
}

// ScanContextSource is ScanContext plus the collector that answered: empty for
// the platform's primary collector, otherwise the name of the alternative that
// did (see chain). A non-empty source means absence from the table is unknown,
// not evidence that a port is free. ScanContextReadSources is the same read when
// a caller wants several collectors merged instead of one that answered.
func ScanContextSource(ctx context.Context) ([]ListeningPort, string, error) {
	read, err := ScanContextRead(ctx, nil)
	return read.Rows, read.Source, err
}

// Read is one pass of a platform's collector chain: the rows that are this
// machine's answer, and the provenance a reader needs before concluding that a
// port is absent.
type Read struct {
	// Rows is the table this read publishes.
	Rows []ListeningPort
	// Source is the one collector that answered instead of the platform's
	// primary one, empty when the primary answered or nothing did. A named source
	// is narrow: its rows are facts, and a port missing from it is unknown.
	Source string
	// Sources is every collector a merged table was assembled from, in chain
	// order. A merged table is never the primary's own answer, so it is narrow
	// for the same reason.
	Sources []string
	// Skip is why the platform's primary collector did not run in this read, nil
	// when it did. A skipped primary is one more reason absence is unknown: the
	// alternatives were read exactly as they would have been, one command
	// cheaper, and no row of the primary's own table was seen.
	Skip *breaker.Skip
}

// Detail is the sentence published with a table that is not the platform's
// primary collector's own whole read: what the collector that answered cannot
// promise, and — when a breaker held the primary back — why it did not run.
// Empty when the primary answered alone.
func (r Read) Detail() string {
	detail := NarrowSourceDetail(r.Source)
	if len(r.Sources) > 0 {
		detail = MergedSourcesDetail(r.Sources)
	}
	clause := r.SkipClause()
	switch {
	case clause == "":
		return detail
	case detail == "":
		return clause
	}
	return detail + "; " + clause
}

// SkipClause is the part of that sentence naming why the primary did not run,
// empty when it did. A verdict that carries its own evidence — a partial pass,
// whose detail is the collector's own failure — adds this clause rather than
// replacing it.
func (r Read) SkipClause() string {
	if r.Skip == nil {
		return ""
	}
	return skipClause(*r.Skip)
}

// ScanContextRead is one collector read under a breaker.
//
// gate is the runtime service's breaker for the platform's primary collector. It
// may be nil, which attempts the primary on every read and records nothing: that
// is what a one-shot CLI wants, because a breaker only means something in a
// process that keeps scanning.
func ScanContextRead(ctx context.Context, gate *breaker.Breaker) (Read, error) {
	return ownedRead(ctx, func(run listenerCommand) (Read, error) {
		return readChain(runtime.GOOS, run, gate)
	})
}

// NarrowSourceDetail is the sentence published with a table read by a collector
// that is not the platform's primary one. It names the limits that collector
// declares and states the consequence a reader needs: a missing port is unknown,
// not free.
func NarrowSourceDetail(source string) string {
	return narrowSourceDetail(runtime.GOOS, source)
}

// ownedRead is the shape every read shares: refuse at once when the caller is
// already gone, run the chain under the caller's ownership, and check that
// ownership again before any row is published. A cancelled command's output is
// never a healthy table, and a partial verdict still travels with its rows.
func ownedRead(ctx context.Context, read func(listenerCommand) (Read, error)) (Read, error) {
	if err := ctx.Err(); err != nil {
		return Read{}, err
	}
	found, err := read(listenerRun(ctx))
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Read{}, ctxErr
	}
	if err != nil {
		partial, ok := PartialRows(err)
		if !ok {
			return Read{Source: found.Source, Skip: found.Skip}, err
		}
		found.Rows = mergeAddresses(partial)
		return found, err
	}
	found.Rows = mergeAddresses(found.Rows)
	return found, nil
}

// listenerRun is the command seam for one context-owned read: every collector
// command runs under the caller's ownership.
func listenerRun(ctx context.Context) listenerCommand {
	return func(name string, args ...string) ([]byte, []byte, error) {
		return executeListenerCommandContext(ctx, name, args...)
	}
}

// scanContextSource is the plain single-table read, without a breaker. The
// self-probe uses it so it tests exactly what a real read does; it must not call
// the probe itself, or a blind process would recurse instead of reporting the
// blind spot.
func scanContextSource(ctx context.Context) ([]ListeningPort, string, error) {
	read, err := ScanContextRead(ctx, nil)
	return read.Rows, read.Source, err
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

// listenerSourceLsofName names the Darwin alternative in a published diagnostic:
// its table is narrower than netstat's (see docs/cli.md), so a reader deciding
// whether an absent port means an idle machine needs to know which collector
// answered. The primary collector of every platform stays unnamed.
const listenerSourceLsofName = "lsof"

// collectListeners never substitutes an empty healthy scan for a missing
// executable or failed collector. Diagnostics make the observation incomplete:
// callers deciding that a listener is absent must not receive a healthy output
// prefix.
func collectListeners(platform string, run listenerCommand) ([]ListeningPort, error) {
	rows, _, err := collectListenersSource(platform, run)
	return rows, err
}

// collectListenersSource walks the platform's collector chain and reports which
// rung answered.
func collectListenersSource(platform string, run listenerCommand) ([]ListeningPort, string, error) {
	read, err := readChain(platform, run, nil)
	return read.Rows, read.Source, err
}

// readChain walks the platform's collector chain and reports what it read.
//
// The primary is unnamed and its answer is the platform's own: nothing else is
// tried once it has spoken. An alternative that answers is named, because a
// reader deciding that a port is absent needs to know which table it was absent
// from. When no rung answers, the primary's failure keeps its code and hint —
// "this process is not allowed to look" stays distinguishable from "the machine
// is idle" — and the other rungs' reasons ride in the same sentence.
//
// gate, when non-nil, decides whether the primary runs at all (see
// attemptPrimary). Skipping it changes nothing a reader may conclude: the
// alternatives are read exactly as they would have been, and the read says out
// loud that the primary did not run.
func readChain(platform string, run listenerCommand, gate *breaker.Breaker) (Read, error) {
	rungs := chain(platform)
	if len(rungs) == 0 {
		return Read{}, fmt.Errorf("unsupported platform: %s", platform)
	}
	rows, err, skip := attemptPrimary(rungs, run, gate)
	if err == nil || (skip == nil && !retryable(rows, err)) {
		return Read{Rows: rows, Skip: skip}, err
	}
	rows, source, err := readAlternatives(rungs[1:], run, err)
	return Read{Rows: rows, Source: source, Skip: skip}, err
}

// readAlternatives reads the rungs after the primary, in order, and reports the
// first one that produced rows. primary is the reason the primary's own table is
// not the answer, and it stays the reported failure when none of them answers.
func readAlternatives(rungs []Collector, run listenerCommand, primary error) ([]ListeningPort, string, error) {
	var others []error
	for _, rung := range rungs {
		rows, err := rung.collect(run)
		if err == nil && len(rows) > 0 {
			return rows, rung.Name, nil
		}
		if err == nil {
			// A collector that ran clean and printed no row is the same
			// suspicious silence the primary reported, not proof of an idle
			// machine: the self-probe stays the one that decides.
			others = append(others, fmt.Errorf("%s: printed no listening table", rung.Name))
			continue
		}
		others = append(others, err)
		if withdrawn(err) {
			// The caller withdrew, or the machine wedged a command. No further
			// collector runs: an answer that arrives now belongs to nobody.
			break
		}
	}
	return nil, "", simultaneousFailure(primary, others...)
}

// retryable reports whether a primary collector left nothing usable, so another
// rung may still answer.
//
// Three shapes qualify: the tool is not installed; it ran and printed no table
// at all (the macOS 27 shape, issue #156); or its pass was invalidated with no
// readable row.
//
// Two shapes never do. A pass that produced readable rows is a fact about this
// machine and stays the answer even though it failed: those rows are published
// with their verdict, and another tool's table must not replace them. A pass the
// decoder marked Partial is the collector's own answer that the table was
// printed but unreadable — no row may survive it — and switching tools would
// hide a decoder problem instead of reporting it. A withdrawn observation
// belongs to the caller, so no second command runs after one.
func retryable(rows []ListeningPort, err error) bool {
	if err == nil || withdrawn(err) {
		return false
	}
	if len(rows) > 0 {
		return false
	}
	var observed *ObservationError
	if errors.As(err, &observed) {
		return !observed.Partial &&
			(observed.Code == CodeObservationEmpty || observed.Code == CodeObservationIncomplete)
	}
	// A bare failure — the executable is not installed, the command was refused
	// before it read anything — has no table behind it, so the next rung may
	// still produce one.
	return true
}

// simultaneousFailure reports every collector that failed. The primary keeps its
// code, so a sandbox that denied netstat is still classified as such; the
// alternatives' reasons go into the same sentence because "netstat saw nothing
// and lsof could not be used" is one fact about this process, not two.
func simultaneousFailure(primary error, fallback ...error) error {
	if len(fallback) == 0 {
		// A platform whose chain has a single collector has nothing to fall back
		// to, so its own reason is the whole report.
		return primary
	}
	reason := failureReasons(fallback)
	var observed *ObservationError
	if errors.As(primary, &observed) {
		return &ObservationError{
			Code:   observed.Code,
			Detail: primary.Error() + "; fallback: " + reason,
			Hint:   observed.Hint,
			Err:    primary,
		}
	}
	return fmt.Errorf("%w; fallback: %s", primary, reason)
}

// failureReasons folds the alternatives' failures into one clause. Each reason
// already names its own collector, so the sentence stays readable however many
// rungs were tried.
func failureReasons(failures []error) string {
	reasons := make([]string, 0, len(failures))
	for _, err := range failures {
		reasons = append(reasons, err.Error())
	}
	return strings.Join(reasons, "; ")
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
