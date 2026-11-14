package runtimecore

import (
	"context"
	"errors"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"testing"
)

func TestReadinessAndGeneration(t *testing.T) {
	for _, tc := range []struct {
		s     state.Service
		ready bool
	}{{state.Service{Name: "worker", Running: true}, true}, {state.Service{Name: "api", Running: true, Port: ptr(80)}, false}, {state.Service{Name: "api", Running: true, Port: ptr(80), PortActual: ptr(80)}, true}, {state.Service{Name: "worker", Running: true, Health: ptr("command:ready")}, false}, {state.Service{Name: "worker", Running: true, Health: ptr("command:ready"), HealthStatus: &state.Health{Status: "ok"}}, true}} {
		g := state.Group{Services: []state.Service{tc.s}}
		if got, _ := Ready(g, nil, tc.s.Name); got != tc.ready {
			t.Fatalf("%+v ready=%v", tc.s, got)
		}
	}
	s := &state.Service{Running: true, RunID: ptr("r2")}
	if _, ok := Replacement("r1", s); !ok || Confirms("r1", s) {
		t.Fatal("replacement confirmed old run")
	}
	if Confirms("r1", &state.Service{}) {
		t.Fatal("unknown id confirmed run")
	}
}
func TestRestartNeverStartsAfterFailedOrCancelledStop(t *testing.T) {
	for _, cancelAfter := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		_, _, err := Restart(ctx, func(context.Context) (int, bool, error) {
			if cancelAfter {
				cancel()
			}
			return 1, cancelAfter, nil
		}, func(context.Context) (int, error) { calls++; return 2, nil })
		cancel()
		if calls != 0 {
			t.Fatal("start escaped failed/cancelled stop")
		}
		if cancelAfter && err != context.Canceled {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	a, b, err := Restart(ctx, func(context.Context) (int, bool, error) { return 1, true, nil }, func(context.Context) (int, error) { return 2, nil })
	if a != 1 || b != 2 || err != nil {
		t.Fatal(a, b, err)
	}
}

func TestReadinessGenerationMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, expected, actual string
		running, want          bool
	}{
		{"same ready", "r1", "r1", true, true}, {"replacement cannot satisfy old wait", "r1", "r2", true, false}, {"unknown identity cannot satisfy", "r1", "", true, false}, {"exited cannot satisfy", "r1", "r1", false, false}, {"legacy unbound wait", "", "r2", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := state.Service{Name: "worker", Running: tc.running}
			if tc.actual != "" {
				row.RunID = ptr(tc.actual)
			}
			ready, _ := Ready(state.Group{Services: []state.Service{row}}, nil, "worker")
			if got := Confirms(tc.expected, &row) && ready; got != tc.want {
				t.Fatalf("ready for selected generation =%v", got)
			}
		})
	}
}

func TestHealthFallbackCannotBorrowAnotherWorktreeListener(t *testing.T) {
	group := state.Group{Name: "a", Services: []state.Service{{Name: "api", Running: true, Port: ptr(8000), PortActual: ptr(8000), Health: ptr("/health")}}}
	rows := []state.Port{{Port: 8000, Group: ptr("b"), Health: &state.Health{Status: state.HealthOK}}}
	if ready, _ := Ready(group, rows, "api"); ready {
		t.Fatal("borrowed healthy listener from another worktree")
	}
	rows[0].Group = ptr("a")
	if ready, _ := Ready(group, rows, "api"); !ready {
		t.Fatal("same-worktree legacy health fallback lost")
	}
}

func TestHealthFallbackBindsHostAndKnownRunIdentity(t *testing.T) {
	group := state.Group{Name: "a", Services: []state.Service{{Name: "api", Running: true, RunID: ptr("r2"), PortActual: ptr(8000), Health: ptr("/health")}}}
	rows := []state.Port{{Port: 8000, Group: ptr("a"), Run: &state.Run{ID: "r1"}, Health: &state.Health{Status: state.HealthOK}}}
	if ready, _ := Ready(group, rows, "api"); ready {
		t.Fatal("old generation health borrowed")
	}
	rows[0].Run.ID = "r2"
	rows[0].Host = "other"
	if ready, _ := Ready(group, rows, "api"); ready {
		t.Fatal("other host health borrowed")
	}
	rows[0].Host = state.LocalhostName
	if ready, _ := Ready(group, rows, "api"); !ready {
		t.Fatal("exact identity healthy listener rejected")
	}
}

func TestRestartRetainsPartialReceiptsAndFailure(t *testing.T) {
	cause := errors.New("synthetic transport failure")
	stopped, started, err := Restart(context.Background(), func(context.Context) (int, bool, error) { return 7, true, nil }, func(context.Context) (int, error) { return 3, cause })
	if stopped != 7 || started != 3 || !errors.Is(err, cause) {
		t.Fatalf("lost partial start/stop receipts: %d %d %v", stopped, started, err)
	}
	calls := 0
	stopped, _, err = Restart(context.Background(), func(context.Context) (int, bool, error) { return 7, false, cause }, func(context.Context) (int, error) { calls++; return 0, nil })
	if stopped != 7 || calls != 0 || !errors.Is(err, cause) {
		t.Fatal("failed stop escaped or lost its receipt")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = Restart(ctx, func(context.Context) (int, bool, error) { calls++; return 0, true, nil }, func(context.Context) (int, error) { calls++; return 0, nil })
	if calls != 0 || !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled request invoked an operation")
	}
}
