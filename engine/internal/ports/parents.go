package ports

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ParentTable returns a pid -> ppid map for every process this user can see.
// It is the same table the scanner builds while enriching listeners, exported
// so the daemon's runs registry can walk a listener's ancestry back to the
// `oberth start` that owns it without scanning ports first.
//
// On Linux the table comes straight from /proc: no exec, no output parsing,
// and — unlike `ps` — nothing to install. procps is absent from plenty of
// container images, and a missing `ps` used to mean every ancestry walk came
// back empty, so a listener a run had spawned was never attributed to it.
// Everywhere else the scan's own process table is reused while it is still
// fresh (the registry asks at its own, shorter cadence — a second `ps -A`
// moments after the scan's would be the same machine read twice). A stale or
// missing scan table falls back to one `ps -A` call.
//
// Windows has neither: no /proc, and no `ps -A` for batchGetProcessTable
// to call, so both of those come back empty and the table used to be empty with
// them — every ancestry walk on Windows failed, and a listener a `oberth start`
// had spawned was never attributed to its run. The fallback is the parents the
// last scan already learned: Get-CimInstance returns ParentProcessId alongside
// the command line it was being asked for anyway, so this costs no process and
// no second query. It covers the pids that have been scanned, which is exactly
// the population the walk starts from.
func ParentTable() map[int]int {
	return collectParentTable(nativeParentTable, recentScanEntries, batchGetProcessTable)
}

// collectParentTable separates source selection from platform I/O. A failed
// refresh must not resurrect arbitrarily old ancestry (PIDs can be reused).
func collectParentTable(native func() map[int]int, recent func() *parentSnapshot, refresh func() map[int]pidEntry) map[int]int {
	if table := native(); len(table) > 0 {
		return table
	}
	if snap := recent(); snap != nil {
		return parentMap(snap.entries)
	}
	info := refresh()
	if len(info) > 0 {
		out := make(map[int]int, len(info))
		for pid, e := range info {
			out[pid] = e.ppid
		}
		return out
	}
	return map[int]int{}
}

// parseProcStatPPID reads the parent pid out of one /proc/<pid>/stat line.
// Field 2 is the executable name in parentheses and may contain spaces and
// parentheses, so parsing starts after the last closing parenthesis.
func parseProcStatPPID(stat string) (int, bool) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil || ppid < 0 {
		return 0, false
	}
	return ppid, true
}

// parentSnapshot is one scan's process facts with the time they were learned,
// so a reader can tell a fresh table from an arbitrarily old one.
type parentSnapshot struct {
	seq     uint64
	at      time.Time
	entries map[int]procEntry
}

// procEntry is the identity the table remembers per pid: who its parent is and
// when it started. The start time is what tells a recycled pid from the
// process a run registry recorded — same integer, different lifetime.
type procEntry struct {
	ppid      int
	startedAt string // raw lstart on unix, RFC3339 from the Windows CIM query
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
func rememberObservedEntries(entries map[int]procEntry, observed processObservation) {
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

// rememberScanParents is for already-observed facts; command callers retain a
// ticket taken before collection and call rememberObservedScanParents instead.
func rememberScanParents(info map[int]procInfo) {
	rememberObservedScanParents(info, beginProcessObservation())
}

func rememberObservedScanParents(info map[int]procInfo, observed processObservation) {
	entries := make(map[int]procEntry, len(info))
	for pid, e := range info {
		if pid > 0 {
			entries[pid] = procEntry{ppid: e.ppid, startedAt: e.startedAt}
		}
	}
	rememberObservedEntries(entries, observed)
}

func rememberParentEntries(info map[int]pidEntry) {
	rememberObservedParentEntries(info, beginProcessObservation())
}

func rememberObservedParentEntries(info map[int]pidEntry, observed processObservation) {
	entries := make(map[int]procEntry, len(info))
	for pid, e := range info {
		if pid > 0 {
			entries[pid] = procEntry{ppid: e.ppid, startedAt: e.startedAt}
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

func processStartWith(pid int, recent func() *parentSnapshot, refresh func() map[int]pidEntry) (time.Time, bool) {
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
func parentMap(entries map[int]procEntry) map[int]int {
	out := make(map[int]int, len(entries))
	for pid, e := range entries {
		if pid > 0 && e.ppid > 0 {
			out[pid] = e.ppid
		}
	}
	return out
}
