package runtimesvc

import (
	"context"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// The read half of the groups namespace. A group is derived state: the scanner
// resolves each port to a project and joins that project's `oberth.yaml`
// services onto it, so listing groups reads the very snapshot the ports read
// uses rather than a second computation that might disagree with it.
func init() {
	registerGroupsReadHandler()
}

func registerGroupsReadHandler() {
	RegisterHandler("groups.list", handleGroupsList)
}

func handleGroupsList(_ context.Context, req *Request) (any, error) {
	rows, err := readGroups(req.HostContext)
	if err != nil {
		return nil, err
	}
	return rpc.GroupsListResult{Groups: rows}, nil
}

// readGroups is readPorts' twin: it is served from the cache while a subscriber
// keeps that cache fresh, and from a scan otherwise.
func readGroups(rt *HostContext) ([]state.Group, error) {
	if rt.Subscribers() > 0 {
		if snap := rt.Scanner.Cached(); snap.Seq > 0 {
			return orEmptyGroups(joinStartAttempts(rt, snap.Groups)), nil
		}
	}
	snap, err := rt.Scanner.Snapshot(scanner.Include{})
	if err != nil {
		return nil, scanFailure(err)
	}
	return orEmptyGroups(joinStartAttempts(rt, snap.Groups)), nil
}

// orEmptyGroups keeps the collection an array on the wire rather than null.
func orEmptyGroups(rows []state.Group) []state.Group {
	if rows != nil {
		return rows
	}
	return []state.Group{}
}
