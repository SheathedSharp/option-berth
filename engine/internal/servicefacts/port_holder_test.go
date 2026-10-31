package servicefacts

import (
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func strPtr(s string) *string { return &s }

func TestFindPortHolderClassifiesTheFailedPort(t *testing.T) {
	const group, other = "shop@wt", "shop@other"
	cases := []struct {
		name      string
		port      state.Port
		kind      state.PortHolderKind
		group     string
		service   string
		runID     string
	}{
		{
			name: "another worktree's managed run",
			port: state.Port{Host: "localhost", Port: 3000, PID: 7, Command: "node",
				Group: strPtr(other), Run: &state.Run{ID: "run-9", Group: other, Name: "api"}},
			kind: state.PortHolderRun, group: other, service: "api", runID: "run-9",
		},
		{
			name: "previous generation of the same service",
			port: state.Port{Host: "localhost", Port: 3000, PID: 7, Command: "node",
				Group: strPtr(group), Run: &state.Run{ID: "run-0", Group: group, Name: "api"}},
			kind: state.PortHolderPrevious, group: group, service: "api", runID: "run-0",
		},
		{
			name: "worktree listener without a managed run",
			port: state.Port{Host: "localhost", Port: 3000, PID: 7, Command: "node",
				Group: strPtr(other)},
			kind: state.PortHolderListener, group: other,
		},
		{
			name: "no worktree attribution",
			port: state.Port{Host: "localhost", Port: 3000, PID: 7, Command: "node"},
			kind: state.PortHolderUnknown,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			holder := FindPortHolder([]state.Port{tt.port}, PortHolderQuery{
				Port: 3000, Group: group, Service: "api", RunID: "run-0",
			})
			if holder == nil {
				t.Fatal("no holder")
			}
			if holder.Kind != tt.kind || holder.Group != tt.group || holder.Name != tt.service || holder.RunID != tt.runID {
				t.Fatalf("holder = %+v, want kind=%s group=%q name=%q run=%q",
					holder, tt.kind, tt.group, tt.service, tt.runID)
			}
			if holder.Port != 3000 || holder.PID != 7 {
				t.Fatalf("holder port/pid = %d/%d", holder.Port, holder.PID)
			}
		})
	}
}

func TestFindPortHolderPrefersTheSameWorktree(t *testing.T) {
	const group, other = "shop@wt", "shop@other"
	ports := []state.Port{
		{Host: "localhost", Port: 3000, PID: 1, Command: "first", Group: strPtr(other)},
		{Host: "localhost", Port: 3000, PID: 2, Command: "second", Group: strPtr(group)},
	}
	holder := FindPortHolder(ports, PortHolderQuery{Port: 3000, Group: group, Service: "api"})
	if holder == nil || holder.PID != 2 {
		t.Fatalf("holder = %+v, want the same-worktree listener", holder)
	}
	// No same-group row: the first listener in input order still answers.
	holder = FindPortHolder(ports[:1], PortHolderQuery{Port: 3000, Group: group, Service: "api"})
	if holder == nil || holder.PID != 1 {
		t.Fatalf("holder = %+v, want the input-order fallback", holder)
	}
}

func TestResolveAttachesTheHolderWithoutMutatingTheSnapshot(t *testing.T) {
	key := Key{Group: "shop", Service: "api"}
	facts := Capture(nil, []ExitRecord{{
		Key: key,
		Exit: state.ServiceExit{Code: 1, Reason: "port_occupied", RunID: "run-1",
			PortHint: 3000},
	}})
	listeners := []state.Port{{
		Host: "localhost", Port: 3000, PID: 7, Command: "node",
		Group: strPtr("shop@other"),
	}}
	result := NewResolver(facts, listeners).Resolve(Declaration{
		Key: key, Port: 4000, // the manifest changed after the failure
	})
	if result.PortHolder == nil || result.PortHolder.Port != 3000 {
		t.Fatalf("holder = %+v, want the exit's own port hint", result.PortHolder)
	}
	if facts[key].LastExit.PortHolder != nil {
		t.Fatal("Resolve mutated the captured exit")
	}
}

func TestResolveIgnoresPortHoldersForOtherExitReasons(t *testing.T) {
	key := Key{Group: "shop", Service: "api"}
	facts := Capture(nil, []ExitRecord{{
		Key: key, Exit: state.ServiceExit{Reason: "crashed", PortHint: 3000},
	}})
	listeners := []state.Port{{Host: "localhost", Port: 3000, PID: 7}}
	if got := NewResolver(facts, listeners).Resolve(Declaration{Key: key, Port: 3000}); got.PortHolder != nil {
		t.Fatalf("holder = %+v, want none for a crash", got.PortHolder)
	}
}
