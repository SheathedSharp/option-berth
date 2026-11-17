package runtimesvc

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func TestRunIDFilterRejectsEmptyAndRepeatedIDs(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		code int
	}{
		{"empty", []string{" "}, rpc.CodeInvalidParams},
		{"repeated", []string{"run-1", "run-1"}, rpc.CodeInvalidParams},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runIDFilter(tc.in)
			var re *rpc.Error
			if err == nil || !errors.As(err, &re) || re.Code != tc.code {
				t.Fatalf("err = %v, want code %d", err, tc.code)
			}
		})
	}
	if got, err := runIDFilter([]string{" run-1 ", "run-2"}); err != nil || !got["run-1"] || !got["run-2"] {
		t.Fatalf("filter = %v/%v", got, err)
	}
}

func TestRunGroupTargetsOnlySelectsTheNamedRuns(t *testing.T) {
	group := "demo"
	snap := state.Snapshot{Ports: []state.Port{
		{Port: 3000, Group: &group, Run: &state.Run{ID: "wanted"}},
		{Port: 3001, Group: &group, Run: &state.Run{ID: "other"}},
		{Port: 3002, Group: &group},
	}}
	targets := runGroupTargets(snap, group, map[string]bool{"wanted": true})
	if len(targets) != 1 || targets[0].Port != 3000 {
		t.Fatalf("targets = %+v, want only the wanted run's listener", targets)
	}
}

func TestSilentGroupStopSelectsExactRunIDs(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	signaled := 0
	reg := &capturedStopFixture{capture: GroupStopCapture{Runs: []GroupStopRun{
		{Owner: state.Run{ID: "wanted", Group: "demo", Name: "web", RootPID: 424241}, StartedAt: at},
		{Owner: state.Run{ID: "other", Group: "demo", Name: "worker", RootPID: 424242}, StartedAt: at},
	}, OnSignal: func(pid int) { signaled = pid }}}
	rt := &HostContext{}
	rt.SetRuns(reg)

	targets, receipt, err := captureSilentGroupStop(rt, state.Snapshot{}, "demo",
		nil, nil, map[string]bool{"wanted": true}, "stopped", false)
	if err != nil || len(targets) != 1 || targets[0].PID != 424241 || receipt == nil {
		t.Fatalf("targets = %+v, err = %v", targets, err)
	}
	receipt(424242)
	if signaled != 0 {
		t.Fatalf("receipt marked an unselected run: pid %d", signaled)
	}
	receipt(424241)
	if signaled != 424241 {
		t.Fatalf("receipt did not mark the selected run: pid %d", signaled)
	}
}

func TestSilentGroupStopRejectsAnUnknownRunIDBeforeSignals(t *testing.T) {
	reg := &capturedStopFixture{capture: GroupStopCapture{Runs: []GroupStopRun{
		{Owner: state.Run{ID: "live", Group: "demo", Name: "web", RootPID: 424241}},
	}}}
	rt := &HostContext{}
	rt.SetRuns(reg)
	_, _, err := captureSilentGroupStop(rt, state.Snapshot{}, "demo",
		nil, nil, map[string]bool{"gone": true}, "stopped", false)
	var re *rpc.Error
	if err == nil || !errors.As(err, &re) || re.Code != rpc.CodeNotFound ||
		!strings.Contains(re.Data.Detail, "gone") {
		t.Fatalf("err = %v, want not_found for the unknown run", err)
	}
}
