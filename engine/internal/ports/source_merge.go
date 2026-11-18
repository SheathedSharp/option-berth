package ports

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/ports/breaker"
)

// ScanContextSources is ScanContextSource extended to a merge of several
// collectors.
//
// limit <= 1 is today's read: the platform's primary table, or the one
// alternative that answered instead of it, whose name travels in
// observation.source. sources is nil there, because one table needs no list.
//
// limit > 1 reads up to limit rungs of the platform's collector chain — never
// more than the chain has — and returns the union of what they saw together
// with the names of the collectors that contributed. Merging is conservative by
// construction: no single collector's authority covers a table assembled this
// way, so the caller publishes every contributor and absence stays unknown. A
// collector that failed contributes no name, which is what keeps its silence
// from turning a missing port into a fact.
func ScanContextSources(ctx context.Context, limit int) ([]ListeningPort, []string, error) {
	read, err := ScanContextReadSources(ctx, nil, limit)
	return read.Rows, read.Sources, err
}

// ScanContextReadSources is ScanContextRead when the runtime service merges
// several collectors (`runtime_service.observe_sources`). The breaker governs
// the primary here too: an open one leaves that rung out of the merge, so its
// name is missing from sources and the read's Skip says why.
func ScanContextReadSources(ctx context.Context, gate *breaker.Breaker, limit int) (Read, error) {
	if limit <= 1 {
		return ScanContextRead(ctx, gate)
	}
	return ownedRead(ctx, func(run listenerCommand) (Read, error) {
		return readChainSources(runtime.GOOS, run, gate, limit)
	})
}

// collectListenersSources reads up to limit rungs of one platform's collector
// chain and reports the union of their tables plus the collectors that
// contributed.
func collectListenersSources(platform string, run listenerCommand, limit int) ([]ListeningPort, []string, error) {
	read, err := readChainSources(platform, run, nil, limit)
	return read.Rows, read.Sources, err
}

// readChainSources is one merged read of the platform's chain, with the
// primary's breaker honoured.
//
// What each rung contributes:
//
//   - readable rows contribute the rows and the name, whether the pass was clean
//     or partial: a row that was read is a fact about this machine, and a
//     partial pass keeps its verdict so the union is published as partial
//     rather than as a clean table. Each of those rows carries the rung's name
//     in Source, so the table-level contributor list never has to be matched
//     against rows that do not say where they came from;
//   - a clean table with no row contributes its name and no rows — the collector
//     looked and saw nothing, which is not the same as not having looked, and
//     the self-probe is still what decides what that silence means;
//   - a hard failure contributes nothing. Its reason is reported only when no
//     rung contributed at all, because a merged table's absence is already
//     unknown and a failed rung must not make it look known;
//   - a primary the breaker held back contributes nothing either way, and its
//     own sentence stands in for its failure so a merge that sees nothing still
//     reports the primary's code.
func readChainSources(platform string, run listenerCommand, gate *breaker.Breaker, limit int) (Read, error) {
	rungs := chain(platform)
	if len(rungs) == 0 {
		return Read{}, fmt.Errorf("unsupported platform: %s", platform)
	}
	// One table is not a merge: the platform's own answer travels in
	// observation.source, and a one-element list would make a primary table look
	// narrow. A one-rung platform stays single however large the limit is.
	if limit <= 1 || len(rungs) == 1 {
		return readChain(platform, run, gate)
	}
	limit = min(limit, len(rungs))
	merged := newRowUnion()
	sources := make([]string, 0, limit)
	var failures, partials []error

	primaryRows, primaryErr, skip := attemptPrimary(rungs, run, gate)
	if withdrawn(primaryErr) {
		return Read{}, primaryErr
	}
	if len(primaryRows) > 0 {
		merged.add(primaryRows, rungs[0].Name)
		sources = append(sources, rungs[0].Name)
		if primaryErr != nil {
			partials = append(partials, primaryErr)
		}
	} else if primaryErr == nil {
		sources = append(sources, rungs[0].Name)
	} else {
		failures = append(failures, primaryErr)
	}

	for _, rung := range rungs[1:limit] {
		rows, err := rung.collect(run)
		if withdrawn(err) {
			// The caller gave up, or the machine wedged a command. An answer
			// that arrives now belongs to nobody, and no further rung runs.
			return Read{}, err
		}
		switch {
		case len(rows) > 0:
			merged.add(rows, rung.Name)
			sources = append(sources, rung.Name)
			if err != nil {
				// A partial pass still read rows, but the rest of its table is
				// unknown. Dropping the error here would publish the union as a
				// clean observation of the whole machine.
				partials = append(partials, err)
			}
		case err == nil:
			sources = append(sources, rung.Name)
		default:
			failures = append(failures, err)
		}
	}
	if len(sources) > 0 {
		rows := merged.rows()
		return Read{Rows: rows, Sources: sources, Skip: skip}, mergedPartial(partials, rows)
	}
	// Every rung that ran either contributed or failed, so an empty source list
	// means the primary's reason is here to keep.
	return Read{Skip: skip}, simultaneousFailure(failures[0], failures[1:]...)
}

// mergedPartial keeps the verdict of any collector whose pass was incomplete.
// The union's readable rows stay facts; the unreadable part of one contributor's
// table is still unknown, and the caller must publish that verdict with the
// rows rather than a clean observation.
func mergedPartial(partials []error, rows []ListeningPort) error {
	if len(partials) == 0 {
		return nil
	}
	first := partials[0]
	detail := first.Error()
	for _, partial := range partials[1:] {
		detail += "; " + partial.Error()
	}
	var observed *ObservationError
	if errors.As(first, &observed) {
		return &ObservationError{
			Code: observed.Code, Detail: detail, Hint: observed.Hint,
			Err: first, Partial: true, Rows: rows,
		}
	}
	return &ObservationError{
		Code: CodeObservationIncomplete, Detail: detail, Hint: observationIncompleteHint,
		Err: first, Partial: true, Rows: rows,
	}
}

// socketID is one socket as two collectors can both name it: one port on one
// address in one address family. It is the merge key, because a second
// collector reporting that socket is reporting the same listener, not another.
type socketID struct {
	port      int
	address   string
	ipVersion string
}

// rowUnion accumulates several collectors' rows in first-seen order, folding
// repeats of one socket.
type rowUnion struct {
	merged   []ListeningPort
	bySocket map[socketID]int
}

func newRowUnion() *rowUnion {
	return &rowUnion{bySocket: make(map[socketID]int)}
}

// add folds one collector's readable rows into the union and names that
// collector on every row it contributes: in a merged table the provenance of a
// row is a fact about the row, and only the union knows which rung it came from.
//
// A duplicate keeps the row that names its owner: a table without the pid still
// proves the port is listening but not who holds it, and learning what the
// other collector missed is the reason to read it. The row that replaces
// another carries its own collector too — the reader must be able to weigh the
// owner against the table that reported it. When both rows name an owner, the
// earlier rung wins — it is the more authoritative table wherever a chain has
// an order, and two owners for one socket is a contradiction this product does
// not resolve by guessing.
func (u *rowUnion) add(rows []ListeningPort, source string) {
	for _, row := range rows {
		row.Source = source
		id := socketID{port: row.Port, address: row.BindAddress, ipVersion: row.IPVersion}
		position, seen := u.bySocket[id]
		if !seen {
			u.bySocket[id] = len(u.merged)
			u.merged = append(u.merged, row)
			continue
		}
		if u.merged[position].PID == 0 && row.PID != 0 {
			u.merged[position] = row
		}
	}
}

// rows is the union with the address fold applied, so one process holding two
// families on one port stays one row exactly as a single collector's table
// reports it.
func (u *rowUnion) rows() []ListeningPort {
	return mergeAddresses(u.merged)
}

// MergedSourcesDetail is the sentence published with a table several collectors
// contributed to. It names every contributor and states the consequence a
// reader needs: a port missing from a merged table is unknown, not free.
func MergedSourcesDetail(sources []string) string {
	if len(sources) == 0 {
		return ""
	}
	return "the listener table was merged from " + strings.Join(sources, ", ") +
		"; a port missing from it is unknown, not free"
}

// withdrawn reports whether the caller ended the observation. Nothing is
// published then, whatever an earlier rung had already answered.
func withdrawn(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
