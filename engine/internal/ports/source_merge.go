package ports

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
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
	if limit <= 1 {
		rows, _, err := scanContextSource(ctx)
		return rows, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	rows, sources, err := collectListenersSources(runtime.GOOS, func(name string, args ...string) ([]byte, []byte, error) {
		return executeListenerCommandContext(ctx, name, args...)
	}, limit)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return rows, sources, err
}

// collectListenersSources reads up to limit rungs of one platform's collector
// chain and reports the union of their tables plus the collectors that
// contributed.
//
// What each rung contributes:
//
//   - readable rows contribute the rows and the name, whether the pass was clean
//     or partial: a row that was read is a fact about this machine;
//   - a clean table with no row contributes its name and no rows — the collector
//     looked and saw nothing, which is not the same as not having looked, and
//     the self-probe is still what decides what that silence means;
//   - a hard failure contributes nothing. Its reason is reported only when no
//     rung contributed at all, because a merged table's absence is already
//     unknown and a failed rung must not make it look known.
func collectListenersSources(platform string, run listenerCommand, limit int) ([]ListeningPort, []string, error) {
	rungs := chain(platform)
	if len(rungs) == 0 {
		return nil, nil, fmt.Errorf("unsupported platform: %s", platform)
	}
	limit = max(1, min(limit, len(rungs)))
	merged := newRowUnion()
	sources := make([]string, 0, limit)
	var failures []error
	for _, rung := range rungs[:limit] {
		rows, err := rung.collect(run)
		if withdrawn(err) {
			// The caller gave up, or the machine wedged a command. An answer
			// that arrives now belongs to nobody, and no further rung runs.
			return nil, nil, err
		}
		switch {
		case len(rows) > 0:
			merged.add(rows)
			sources = append(sources, rung.Name)
		case err == nil:
			sources = append(sources, rung.Name)
		default:
			failures = append(failures, err)
		}
	}
	if len(sources) > 0 {
		return merged.rows(), sources, nil
	}
	// At least one rung ran, and every rung that ran either contributed or
	// failed, so an empty source list means the primary's reason is here to keep.
	return nil, nil, simultaneousFailure(failures[0], failures[1:]...)
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

// add folds one collector's readable rows into the union.
//
// A duplicate keeps the row that names its owner: a table without the pid still
// proves the port is listening but not who holds it, and learning what the
// other collector missed is the reason to read it. When both rows name an
// owner, the earlier rung wins — it is the more authoritative table wherever a
// chain has an order, and two owners for one socket is a contradiction this
// product does not resolve by guessing.
func (u *rowUnion) add(rows []ListeningPort) {
	for _, row := range rows {
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
