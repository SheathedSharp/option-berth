package runtimesvc

import (
	"fmt"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// The read half of the ports namespace. Each handler answers from the scanner's
// shared snapshot instead of scanning for itself, so N clients reading at once
// still cost the machine one scan (see readPorts).
func init() {
	RegisterHandler("ports.logs", handlePortsLogs)
}

// readPorts serves the rows every read handler starts from.
//
// With subscribers connected the scan loop is already running, so the cached
// snapshot is at most one scan interval old and a plain read can be answered
// from it; that is what keeps the scan counter flat — an `oberth status` beside
// a running `oberth watch` costs nothing, and ten of them cost nothing either.
// A read asking for stats or health cannot use the cache unless a subscriber is
// already collecting them, and a read with no loop behind it scans for itself
// (reusing a scan younger than scanner.CacheTTL).
func readPorts(rt *HostContext, include scanner.Include) ([]state.Port, error) {
	snap, err := readSnapshot(rt, include)
	if err != nil {
		return nil, err
	}
	return snap.Ports, nil
}

// readSnapshot is readPorts plus the observation the rows came with: a caller
// deciding whether an absent port is free needs the table's coverage, not just
// its rows.
func readSnapshot(rt *HostContext, include scanner.Include) (state.Snapshot, error) {
	if rt.Subscribers() > 0 {
		if snap := rt.Scanner.Cached(); snap.Seq > 0 && cacheCovers(snap, include) {
			return snap, nil
		}
	}
	snap, err := rt.Scanner.Snapshot(include)
	if err != nil {
		return state.Snapshot{}, scanFailure(err)
	}
	return snap, nil
}

// cacheCovers reports whether the cached snapshot was collected with the
// enrichments this read asks for. An empty port table covers everything
// trivially: there is nothing to enrich.
//
// The question is about the snapshot as a whole, not about every row in it, and
// asking it row by row was wrong. `stats` is null for a process whose numbers
// are all zero — a listener ps cannot see, a container the runtime service has no
// reading for — and `health` is null for anything not probed, so on a machine
// with one such row every stats read missed the cache and scanned the machine
// again, while a subscriber was already collecting stats every second. One row
// carrying the enrichment is what proves the scan behind this snapshot
// collected it.
func cacheCovers(snap state.Snapshot, include scanner.Include) bool {
	wantsStats, wantsHealth := include.Stats, include.Health
	if !wantsStats && !wantsHealth {
		return true
	}
	if len(snap.Ports) == 0 {
		return true
	}
	var hasStats, hasHealth bool
	for i := range snap.Ports {
		hasStats = hasStats || snap.Ports[i].Stats != nil
		hasHealth = hasHealth || snap.Ports[i].Health != nil
	}
	return (!wantsStats || hasStats) && (!wantsHealth || hasHealth)
}

// resolvePort finds the single row a selector addresses. A port bound to
// several addresses without a bind_address is ambiguous (1002), never a silent
// pick.
func resolvePort(rows []state.Port, sel rpc.Selector) (state.Port, error) {
	if sel.PID != nil {
		return portForPID(rows, *sel.PID)
	}
	if sel.Port == nil {
		return state.Port{}, rpc.NewError(rpc.CodeInvalidParams,
			"a selector needs a port or a pid", `send {"port": 3000} or {"pid": 1234}`)
	}
	matches := rowsForPort(rows, *sel.Port, sel.BindAddress)
	switch len(matches) {
	case 0:
		return state.Port{}, portNotFound(*sel.Port, sel.BindAddress)
	case 1:
		return matches[0], nil
	}
	return state.Port{}, ambiguousPort(*sel.Port, matches)
}

// portForPID returns the first row owned by pid.
func portForPID(rows []state.Port, pid int) (state.Port, error) {
	var matches []state.Port
	for _, row := range rows {
		if row.PID == pid {
			matches = append(matches, row)
		}
	}
	if len(matches) == 0 {
		return state.Port{}, rpc.NewError(rpc.CodeNotFound,
			fmt.Sprintf("no listening port found for pid %d", pid),
			"run `oberth status` to see what is listening")
	}
	return matches[0], nil
}

// rowsForPort selects the rows on port, narrowed to bindAddress when the
// selector names one.
func rowsForPort(rows []state.Port, port int, bindAddress *string) []state.Port {
	var matches []state.Port
	for _, row := range rows {
		if row.Port != port {
			continue
		}
		if bindAddress != nil && *bindAddress != "" && row.BindAddress != *bindAddress {
			continue
		}
		matches = append(matches, row)
	}
	return matches
}

func portNotFound(port int, bindAddress *string) error {
	if bindAddress != nil && *bindAddress != "" {
		return rpc.NewError(rpc.CodeNotFound,
			fmt.Sprintf("no process found listening on %s:%d", *bindAddress, port), "")
	}
	return rpc.NewError(rpc.CodeNotFound,
		fmt.Sprintf("no process found listening on port %d", port),
		"run `oberth status` to see what is listening")
}

func ambiguousPort(port int, matches []state.Port) error {
	addrs := make([]string, 0, len(matches))
	for _, m := range matches {
		addrs = append(addrs, m.BindAddress)
	}
	return rpc.NewError(rpc.CodeAmbiguous,
		fmt.Sprintf("port %d is bound to multiple addresses: %s", port, strings.Join(addrs, ", ")),
		fmt.Sprintf("pass a bind address (e.g. --ip %s)", addrs[0]))
}
