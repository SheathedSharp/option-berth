package daemon

import (
	"fmt"
	"strings"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/scanner"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// The read half of the ports namespace. Every handler here answers from the
// scanner's shared snapshot rather than scanning for itself, so N clients
// reading at once cost the machine one scan, not N. See readPorts.
func init() {
	RegisterHandler("ports.logs", handlePortsLogs)
}

// readPorts serves the rows every read handler starts from.
//
// While subscribers are connected the scan loop is already running, so the
// cached snapshot is never more than one scan interval old and a plain read is
// answered from it. That is what keeps the scan counter flat: a `oberth status`
// next to a running `oberth watch` costs nothing, and ten of them cost nothing
// either. A read that asks for stats or health cannot use the cache unless a
// subscriber is already collecting them, and a read with no loop behind it
// scans for itself (reusing a scan younger than scanner.CacheTTL).
func readPorts(rt *Runtime, include scanner.Include) ([]state.Port, error) {
	if rt.Subscribers() > 0 {
		if snap := rt.Scanner.Cached(); snap.Seq > 0 && cacheCovers(snap, include) {
			return snap.Ports, nil
		}
	}
	snap, err := rt.Scanner.Snapshot(include)
	if err != nil {
		return nil, rpc.NewError(rpc.CodeInternal, "scan failed: "+err.Error(),
			"check `oberth daemon log` for the scanner error")
	}
	return snap.Ports, nil
}

// cacheCovers reports whether the cached snapshot was collected with the
// enrichments this read asked for. An empty port table trivially covers
// everything: there is nothing to enrich.
//
// The question is about the snapshot, not about every row in it, and asking it
// row by row was wrong. `stats` is null for a process whose numbers are all
// zero — a listener ps cannot see, a container the daemon has no reading for —
// and `health` is null for anything that was not probed, which on a machine
// with one such row made *every* stats read miss the cache and scan the
// machine again, while a subscriber was already collecting stats every second
// (contract §42, §44). One row carrying the enrichment is what says the scan
// behind this snapshot collected it.
func cacheCovers(snap state.Snapshot, include scanner.Include) bool {
	if !include.Stats && !include.Health {
		return true
	}
	if len(snap.Ports) == 0 {
		return true
	}
	stats, health := false, false
	for i := range snap.Ports {
		stats = stats || snap.Ports[i].Stats != nil
		health = health || snap.Ports[i].Health != nil
	}
	return (!include.Stats || stats) && (!include.Health || health)
}

// resolvePort finds the one row a selector addresses. A port bound to several
// addresses without a bind_address is ambiguous (1002), not a silent pick.
func resolvePort(rows []state.Port, sel rpc.Selector) (state.Port, error) {
	var matches []state.Port
	switch {
	case sel.PID != nil:
		for _, row := range rows {
			if row.PID == *sel.PID {
				matches = append(matches, row)
			}
		}
		if len(matches) == 0 {
			return state.Port{}, rpc.NewError(rpc.CodeNotFound,
				fmt.Sprintf("no listening port found for pid %d", *sel.PID),
				"run `oberth status` to see what is listening")
		}
		return matches[0], nil
	case sel.Port != nil:
		for _, row := range rows {
			if row.Port != *sel.Port {
				continue
			}
			if sel.BindAddress != nil && *sel.BindAddress != "" && row.BindAddress != *sel.BindAddress {
				continue
			}
			matches = append(matches, row)
		}
	default:
		return state.Port{}, rpc.NewError(rpc.CodeInvalidParams,
			"a selector needs a port or a pid", `send {"port": 3000} or {"pid": 1234}`)
	}

	switch len(matches) {
	case 0:
		if sel.BindAddress != nil && *sel.BindAddress != "" {
			return state.Port{}, rpc.NewError(rpc.CodeNotFound,
				fmt.Sprintf("no process found listening on %s:%d", *sel.BindAddress, *sel.Port), "")
		}
		return state.Port{}, rpc.NewError(rpc.CodeNotFound,
			fmt.Sprintf("no process found listening on port %d", *sel.Port),
			"run `oberth status` to see what is listening")
	case 1:
		return matches[0], nil
	default:
		addrs := make([]string, 0, len(matches))
		for _, m := range matches {
			addrs = append(addrs, m.BindAddress)
		}
		return state.Port{}, rpc.NewError(rpc.CodeAmbiguous,
			fmt.Sprintf("port %d is bound to multiple addresses: %s", *sel.Port, strings.Join(addrs, ", ")),
			fmt.Sprintf("pass a bind address (e.g. --ip %s)", addrs[0]))
	}
}
