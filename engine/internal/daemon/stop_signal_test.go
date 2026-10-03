package daemon

import (
	"reflect"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/killer"
	"github.com/sheathedsharp/option-berth/internal/state"
)

type receiptRegistry struct {
	fakeRuns
	owners []state.Run
	reason string
	calls  int
}

func (r *receiptRegistry) StopSignalObserver(owners []state.Run, reason string) func(int) {
	r.owners, r.reason = owners, reason
	r.calls++
	return func(int) {}
}
func (*receiptRegistry) Stopping([]int) { panic("speculative stop marker") }

func TestStopSignalOwnersFollowSelectedRuntime(t *testing.T) {
	one := state.Run{RootPID: 10, ID: "one", Group: "example", Name: "api"}
	two := state.Run{RootPID: 30, ID: "two", Group: "other", Name: "worker"}
	snap := state.Snapshot{Ports: []state.Port{
		{Port: 3000, BindAddress: "127.0.0.1", PID: 20, Run: &one},
		{Port: 3000, BindAddress: "::1", PID: 30, Run: &two},
		{Port: 4000, PID: 40, Host: "remote", Run: &two},
	}}
	for _, item := range []struct {
		target killer.Target
		want   []state.Run
	}{
		{killer.Target{Port: 3000, BindAddress: "127.0.0.1"}, []state.Run{one}},
		{killer.Target{PID: 20}, []state.Run{one}},
		{killer.Target{PID: 90}, []state.Run{{RootPID: 90}}},
		{killer.Target{RunID: "one"}, []state.Run{one}},
		{killer.Target{Port: 4000}, nil},
	} {
		if got := stopSignalOwners(snap, []killer.Target{item.target}); !reflect.DeepEqual(got, item.want) {
			t.Fatalf("target=%+v owners=%+v want=%+v", item.target, got, item.want)
		}
	}
}

func TestStopSignalObserverIsOptionalAndNeverPremarks(t *testing.T) {
	rt := &Runtime{}
	if stopSignalObserver(rt, state.Snapshot{}, nil, false, "") != nil {
		t.Fatal("noRuns unexpectedly observes")
	}
	reg := &receiptRegistry{}
	rt.SetRuns(reg)
	if stopSignalObserver(rt, state.Snapshot{}, nil, true, "ready_timeout") != nil || reg.calls != 0 {
		t.Fatal("dry run prepared a mutation")
	}
	observer := stopSignalObserver(rt, state.Snapshot{}, []killer.Target{{PID: 42}}, false, "ready_timeout")
	if observer == nil || reg.calls != 1 || reg.reason != "ready_timeout" || !reflect.DeepEqual(reg.owners, []state.Run{{RootPID: 42}}) {
		t.Fatalf("receipt wiring=%+v", reg)
	}
}
