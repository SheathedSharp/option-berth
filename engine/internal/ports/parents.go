package ports

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ParentTable maps every visible process to its parent. The scanner builds the
// same table while enriching listeners; exporting it lets the daemon's run
// registry walk a listener's ancestry back to the `oberth start` that owns it
// without scanning ports first.
//
// Linux reads /proc directly: no exec, no parsing of command output, and
// nothing to install. Plenty of container images lack procps, and a missing
// `ps` used to leave every ancestry walk empty, so a listener a run spawned
// was never attributed to it.
//
// On other Unix systems a fresh scan table is reused (the registry asks at a
// shorter cadence, and a second `ps -A` moments later would read the same
// machine twice); a stale or missing table falls back to one `ps -A`.
//
// Windows has neither /proc nor `ps -A`, so the fallback is what the last scan
// learned: the CIM query already returns ParentProcessId beside the command
// line, so the table costs no extra process and covers exactly the pids the
// walk starts from.
func ParentTable() map[int]int {
	return collectParentTable(nativeParentTable, recentScanEntries, batchGetProcessTable)
}

// collectParentTable separates source selection from platform I/O. A failed
// refresh must not resurrect arbitrarily old ancestry (PIDs can be reused).
func collectParentTable(native func() map[int]int, recent func() *parentSnapshot, refresh func() map[int]processFacts) map[int]int {
	if table := native(); len(table) > 0 {
		return table
	}
	if snap := recent(); snap != nil {
		return parentMap(snap.entries)
	}
	info := refresh()
	if len(info) > 0 {
		parents := make(map[int]int, len(info))
		for pid, entry := range info {
			parents[pid] = entry.ppid
		}
		return parents
	}
	return map[int]int{}
}

// parseProcStatPPID reads the parent pid out of one /proc/<pid>/stat line.
// Field 2 is the executable name in parentheses and may contain spaces and
// parentheses, so parsing starts after the last closing parenthesis.
func parseProcStatPPID(stat string) (int, bool) {
	closing := strings.LastIndexByte(stat, ')')
	if closing < 0 {
		return 0, false
	}
	columns := strings.Fields(stat[closing+1:])
	if len(columns) < 2 {
		return 0, false
	}
	parent, err := strconv.Atoi(columns[1])
	if err != nil || parent < 0 {
		return 0, false
	}
	return parent, true
}

// parentSnapshot is one scan's process facts with the time they were learned,
// so a reader can tell a fresh table from an arbitrarily old one.
type parentSnapshot struct {
	seq     uint64
	at      time.Time
	entries map[int]processFacts
}

// processFacts is what one observation of the process table says about one pid:
// its parent, the command line the source reported, and when it started. The
// start time is what tells a recycled pid from the process a run registry
// recorded — same integer, different lifetime. startedAt keeps the source's own
// text (raw lstart on unix, RFC3339 from the Windows CIM query) and is parsed
// only where a reader needs an instant.
type processFacts struct {
	ppid      int
	command   string
	startedAt string
}

// scanParentTable holds the pid facts the most recent scan learned, for
// platforms with no process table of their own to read and for the registry's
// shorter-cadence lookups on every platform. It is replaced whole, never
// mutated, so a reader always sees one consistent scan's worth.
var scanParentTable atomic.Pointer[parentSnapshot]

// processObservation identifies admission, not completion time. All process
// cache writers take a ticket before IO; a delayed older observation cannot
// replace newer process identity, even when wall-clock timestamps are equal.
// This is one monotonic counter, not another process table or a work queue.
type processObservation struct {
	seq uint64
	at  time.Time
}

var processObservationSeq atomic.Uint64

func beginProcessObservation() processObservation {
	return processObservation{seq: processObservationSeq.Add(1), at: time.Now()}
}

// rememberObservedEntries publishes immutable facts only in admission order.
// Empty observations preserve the last table without renewing its age. The age
// starts before IO, so slow collection does not grant stale facts a new TTL.
func rememberObservedEntries(entries map[int]processFacts, observed processObservation) {
	if len(entries) == 0 {
		return
	}
	next := &parentSnapshot{seq: observed.seq, at: observed.at, entries: entries}
	for {
		previous := scanParentTable.Load()
		if previous != nil && previous.seq >= observed.seq {
			return
		}
		if scanParentTable.CompareAndSwap(previous, next) {
			return
		}
	}
}

// rememberProcessFacts is for facts already observed; a caller that collected
// them itself keeps a ticket taken before the IO and calls
// rememberObservedProcessFacts instead, so a delayed older table cannot
// replace a newer one.
func rememberProcessFacts(info map[int]processFacts) {
	rememberObservedProcessFacts(info, beginProcessObservation())
}

// rememberObservedProcessFacts publishes one observation under its admission
// ticket. The map is copied so a later mutation by the collector cannot change
// what a reader already received.
func rememberObservedProcessFacts(info map[int]processFacts, observed processObservation) {
	entries := make(map[int]processFacts, len(info))
	for pid, facts := range info {
		if pid > 0 {
			entries[pid] = facts
		}
	}
	rememberObservedEntries(entries, observed)
}

// recentScanEntries returns the last scan's process facts while they are still
// within the scan cache's freshness window, or nil when they are older. The
// window is the same one the enrichment cache promises, so every consumer of
// this table works from one freshness rule.
func recentScanEntries() *parentSnapshot {
	return freshParentEntries(scanParentTable.Load(), time.Now())
}

func freshParentEntries(snap *parentSnapshot, now time.Time) *parentSnapshot {
	if snap == nil || snap.at.IsZero() || now.Before(snap.at) || now.Sub(snap.at) >= displaySignalCacheTTL {
		return nil
	}
	return snap
}

// StartTolerance absorbs the slack between a recorded run start and the
// process table's lstart: one-second granularity plus the moments between fork
// and registration. A recycled pid is a different process entirely, so it
// starts far later than this, never a second or two. Both run registries use
// the same tolerance so they agree on what counts as reuse.
const StartTolerance = 2 * time.Second

// ProcessStart reports when the process behind pid started, from the scan's
// process table. It is the identity check a run registry uses to tell a
// recycled pid from the process it recorded. ok is false when the table has no
// answer — unknown, not recycled.
func ProcessStart(pid int) (time.Time, bool) {
	return processStartWith(pid, recentScanEntries, batchGetProcessTable)
}

func processStartWith(pid int, recent func() *parentSnapshot, refresh func() map[int]processFacts) (time.Time, bool) {
	if pid <= 0 {
		return time.Time{}, false
	}
	snap := recent()
	if snap == nil {
		_ = refresh()   // may fail or report only a partial process table
		snap = recent() // the retained last-good table is not fresh evidence
	}
	if snap == nil {
		return time.Time{}, false
	}
	e, ok := snap.entries[pid]
	if !ok || e.startedAt == "" {
		return time.Time{}, false
	}
	// parseStartTime normalizes both the unix raw lstart and the RFC3339 the
	// Windows CIM query reports.
	parsed := parseStartTime(e.startedAt)
	if parsed == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, parsed)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// scanParents returns a copy of what the last scan learned, however old.
func scanParents() map[int]int {
	snap := scanParentTable.Load()
	if snap == nil {
		return map[int]int{}
	}
	return parentMap(snap.entries)
}

// parentMap projects process facts onto the pid -> ppid map the ancestry walks
// need. Entries without a parent are not part of a parent map.
func parentMap(entries map[int]processFacts) map[int]int {
	out := make(map[int]int, len(entries))
	for pid, e := range entries {
		if pid > 0 && e.ppid > 0 {
			out[pid] = e.ppid
		}
	}
	return out
}
