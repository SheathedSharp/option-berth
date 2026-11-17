package runtimesvc

import (
	"errors"
	"fmt"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/killer"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// GroupStopRun is an internal control identity, not an RPC/schema extension.
type GroupStopRun struct {
	Owner     state.Run
	StartedAt time.Time
}

// GroupStopCapture fixes identities and receipts together before any signal.
type GroupStopCapture struct {
	Runs     []GroupStopRun
	OnSignal func(int)
}

type groupStopCapturer interface {
	CaptureGroupStop(group, reason string) GroupStopCapture
}

// captureSilentGroupStop selects the runs that hold no port. only selects by
// service name, expect by service/run identity and runs by exact run id; the
// caller never passes more than one, and whichever is set narrows the list
// before any identity is captured.
func captureSilentGroupStop(rt *HostContext, snap state.Snapshot, group string, only map[string]bool, expect map[string]string, runs map[string]bool, reason string, dryRun bool) ([]killer.Target, func(int), error) {
	reg := rt.Runs()
	capturer, ok := reg.(groupStopCapturer)
	if !ok {
		if len(runs) > 0 {
			return nil, nil, rpc.NewError(rpc.CodeInternal,
				"registry cannot resolve exact run ids safely",
				"stop by port or pid with `oberth kill`")
		}
		// Preserve read-only compatibility. A legacy registry may list PIDs, but
		// separate GroupRuns/RunStart calls cannot pin their generation atomically.
		pids := reg.GroupPIDs(group)
		if len(only) > 0 || len(expect) > 0 {
			pids = groupPIDs(reg.GroupRuns(group), only, expect)
		}
		pids = runsWithoutPorts(snap, group, pids)
		if len(pids) > 0 && !dryRun {
			return nil, nil, errors.New("registry cannot capture no-port run identities safely")
		}
		targets := make([]killer.Target, 0, len(pids))
		for _, pid := range pids {
			targets = append(targets, killer.Target{PID: pid})
		}
		return targets, nil, nil
	}
	captured := capturer.CaptureGroupStop(group, reason)
	identities := make(map[int]GroupStopRun, len(captured.Runs))
	pids := make([]int, 0, len(captured.Runs))
	matched := make(map[string]bool, len(runs))
	for _, run := range captured.Runs {
		if len(only) > 0 && !only[run.Owner.Name] {
			continue
		}
		if len(expect) > 0 && expect[run.Owner.Name] != run.Owner.ID {
			continue
		}
		if len(runs) > 0 {
			if !runs[run.Owner.ID] {
				continue
			}
			matched[run.Owner.ID] = true
		}
		if run.Owner.RootPID <= 0 {
			return nil, nil, errors.New("invalid captured run PID")
		}
		if _, duplicate := identities[run.Owner.RootPID]; duplicate {
			return nil, nil, errors.New("ambiguous captured run PID")
		}
		identities[run.Owner.RootPID] = run
		pids = append(pids, run.Owner.RootPID)
	}
	if len(runs) > 0 {
		for id := range runs {
			if !matched[id] {
				return nil, nil, rpc.NewError(rpc.CodeNotFound,
					fmt.Sprintf("run %q is not live in group %s", id, group),
					"run `oberth status --json` to list the live run ids")
			}
		}
	}
	targets := make([]killer.Target, 0, len(pids))
	for _, pid := range runsWithoutPorts(snap, group, pids) {
		run := identities[pid]
		if run.StartedAt.IsZero() && !dryRun {
			return nil, nil, errors.New("no-port run has no recorded birth identity; refusing PID-only stop")
		}
		targets = append(targets, killer.Target{PID: pid, StartedAt: run.StartedAt, Name: run.Owner.Name})
	}
	if dryRun {
		return targets, nil, nil
	}
	onSignal := captured.OnSignal
	if len(runs) > 0 {
		// The capture marks every run it captured; an exact-run stop must mark
		// only the ones it selected.
		selected := make(map[int]bool, len(identities))
		for pid := range identities {
			selected[pid] = true
		}
		base := captured.OnSignal
		onSignal = func(pid int) {
			if selected[pid] {
				base(pid)
			}
		}
	}
	return targets, onSignal, nil
}
