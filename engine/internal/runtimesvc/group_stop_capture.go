package runtimesvc

import (
	"errors"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/killer"
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
// service name and expect by run identity; the caller never passes both, and
// whichever is set narrows the list before any identity is captured.
func captureSilentGroupStop(rt *HostContext, snap state.Snapshot, group string, only map[string]bool, expect map[string]string, reason string, dryRun bool) ([]killer.Target, func(int), error) {
	reg := rt.Runs()
	capturer, ok := reg.(groupStopCapturer)
	if !ok {
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
	for _, run := range captured.Runs {
		if len(only) > 0 && !only[run.Owner.Name] {
			continue
		}
		if len(expect) > 0 && expect[run.Owner.Name] != run.Owner.ID {
			continue
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
	return targets, captured.OnSignal, nil
}
