package runstate

import (
	"context"
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
