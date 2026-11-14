package runtimesvc

import (
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

type capturedStopFixture struct {
	noRuns
	capture GroupStopCapture
}

func (r *capturedStopFixture) CaptureGroupStop(string, string) GroupStopCapture { return r.capture }
func (*capturedStopFixture) RunStart(int) (time.Time, bool) {
	panic("must not re-read a replacement generation")
}

func TestSilentGroupStopUsesCapturedBirthAndScope(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	signaled := 0
	reg := &capturedStopFixture{capture: GroupStopCapture{Runs: []GroupStopRun{
		{Owner: state.Run{ID: "listener", Group: "demo", Name: "web", RootPID: 424241}, StartedAt: at},
		{Owner: state.Run{ID: "worker", Group: "demo", Name: "worker", RootPID: 424242}, StartedAt: at},
		{Owner: state.Run{ID: "unselected", Group: "demo", Name: "other", RootPID: 424243}, StartedAt: at},
	}, OnSignal: func(pid int) { signaled = pid }}}
	rt := &Runtime{}
	rt.SetRuns(reg)
	snap := state.Snapshot{Ports: []state.Port{{PID: 424241, Port: 12501, Group: ptr("demo")}}}
	targets, receipt, err := captureSilentGroupStop(rt, snap, "demo", map[string]bool{"web": true, "worker": true}, nil, "stopped", false)
	if err != nil || len(targets) != 1 || targets[0].PID != 424242 || targets[0].Name != "worker" || !targets[0].StartedAt.Equal(at) || receipt == nil {
		t.Fatalf("targets=%+v error=%v", targets, err)
	}
	receipt(424242)
	if signaled != 424242 {
		t.Fatal("captured receipt was not used")
	}
	_, receipt, err = captureSilentGroupStop(rt, snap, "demo", nil, nil, "stopped", true)
	if err != nil || receipt != nil {
		t.Fatalf("dry run receipt=%v error=%v", receipt != nil, err)
	}
}

// A timeout cleanup names the run it started. When that service was restarted
// in the meantime, the replacement run answers to the same service name and
// holds no port — it must not be captured for the stop.
func TestSilentGroupStopFollowsTheExpectedRunNotTheService(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	reg := &capturedStopFixture{capture: GroupStopCapture{Runs: []GroupStopRun{
		{Owner: state.Run{ID: "run-timed-out", Group: "demo", Name: "worker", RootPID: 424242}, StartedAt: at},
		{Owner: state.Run{ID: "run-replacement", Group: "demo", Name: "worker", RootPID: 424243}, StartedAt: at},
	}}}
	rt := &Runtime{}
	rt.SetRuns(reg)
	targets, _, err := captureSilentGroupStop(rt, state.Snapshot{}, "demo", nil,
		map[string]string{"worker": "run-timed-out"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].PID != 424242 {
		t.Fatalf("targets = %+v, want only the run the caller started", targets)
	}
}

func TestSilentGroupStopRejectsMissingIdentityBeforeSignals(t *testing.T) {
	rt := &Runtime{}
	rt.SetRuns(&capturedStopFixture{capture: GroupStopCapture{Runs: []GroupStopRun{{Owner: state.Run{RootPID: 424242}}}}})
	targets, receipt, err := captureSilentGroupStop(rt, state.Snapshot{}, "demo", nil, nil, "", false)
	if err == nil || len(targets) != 0 || receipt != nil {
		t.Fatalf("targets=%+v error=%v", targets, err)
	}
}
