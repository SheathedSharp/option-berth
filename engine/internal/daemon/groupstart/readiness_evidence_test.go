package groupstart

import (
	"testing"

	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestPortDependencyRequiresObservedListenerNotJustRun(t *testing.T) {
	port := 4100
	for _, auto := range []bool{false, true} {
		dep := groups.Service{Name: "db", Port: port, PortAuto: auto}
		snap := state.Snapshot{Groups: []state.Group{{
			Name: "project", Services: []state.Service{{Name: "db", Running: true}},
		}}}
		if listening(snap, "project", dep, port) {
			t.Fatal("registered run without listener satisfied a ported dependency")
		}
		actual := port + 1 // the attributed service may bind a different port
		snap.Groups[0].Services[0].PortActual = &actual
		if !listening(snap, "project", dep, port) {
			t.Fatal("observed service listener did not satisfy the dependency")
		}
		actual = 0
		if listening(snap, "project", dep, port) {
			t.Fatal("zero actual port became listener evidence")
		}
	}
}

func TestDependencyRawPortCannotOverridePublishedServiceVerdict(t *testing.T) {
	group, port := "project", 4100
	dep := groups.Service{Name: "db", PortAuto: true}
	snap := state.Snapshot{
		Groups: []state.Group{{Name: group, Services: []state.Service{{Name: "db", Running: false}}}},
		Ports:  []state.Port{{Port: port, PID: 42, Group: &group}},
	}
	if listening(snap, group, dep, port) {
		t.Fatal("raw listener bypassed service identity or exit filtering")
	}
}

func TestDependencyRawPortRequiresExplicitLocalGroup(t *testing.T) {
	group, foreign, port := "project", "other-worktree", 4100
	dep := groups.Service{Name: "db", PortAuto: true}
	cases := []struct {
		name string
		row  state.Port
		want bool
	}{
		{"own unmanaged listener", state.Port{Port: port, Group: &group}, true},
		{"other worktree", state.Port{Port: port, Group: &foreign}, false},
		{"no attribution", state.Port{Port: port}, false},
		{"other host", state.Port{Port: port, Group: &group, Host: "remote"}, false},
		{"other service", state.Port{Port: port, Group: &group, Run: &state.Run{Group: group, Name: "worker"}}, false},
		{"contradictory run group", state.Port{Port: port, Group: &group, Run: &state.Run{Group: foreign, Name: "db"}}, false},
		{"own run", state.Port{Port: port, Group: &group, Run: &state.Run{Group: group, Name: "db"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := listening(state.Snapshot{Ports: []state.Port{tc.row}}, group, dep, port); got != tc.want {
				t.Fatalf("readiness=%v, want %v", got, tc.want)
			}
		})
	}
	if listening(state.Snapshot{}, group, dep, 0) {
		t.Fatal("unallocated auto port became ready")
	}
}
