package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// checkListenerObservation answers "can this process see its own listeners?".
// It binds a real local port and asks the same collector a status read uses
// whether that listener is visible as this process. In CLI mode it also reports
// the runtime service's own probe: a healthy CLI cannot make a blind runtime service's empty port
// list trustworthy.
func checkListenerObservation(ctx context.Context, env *Env) rpc.DoctorCheck {
	ctx, cancel := context.WithTimeout(ctx, NetworkBudget)
	defer cancel()

	cli := env.ProbeObservation(ctx)
	parts := []string{observationPart("cli", cli.State, cli.Detail)}
	fix := cli.Hint

	var runtimeService *rpc.ObservationStatus
	if env.Mode == ModeCLI {
		if info := env.RuntimeService(ctx); info.Reachable {
			runtimeService = info.Observation
			if runtimeService == nil {
				parts = append(parts, "runtime_service: observation not reported")
			} else {
				parts = append(parts, observationPart("runtime_service", observationState(runtimeService.State), runtimeService.Detail))
			}
		}
	}

	detail := strings.Join(parts, "; ")
	switch {
	case cli.State == ports.ObservationBindDenied:
		return fail("this process cannot bind a local port", detail, fix)
	case cli.State != ports.ObservationObservable:
		return fail("this process cannot see its own listener", detail, fix)
	case runtimeService != nil && runtimeService.State != string(ports.ObservationObservable):
		if runtimeService.Hint != "" {
			fix = runtimeService.Hint
		}
		return fail("the runtime service cannot see its own listeners", detail, fix)
	}
	return ok("this process can see its own listener", detail)
}

func observationState(state string) ports.ObservationState {
	if state == "" {
		return "unknown"
	}
	return ports.ObservationState(state)
}

func observationPart(label string, state ports.ObservationState, detail string) string {
	if detail == "" {
		return fmt.Sprintf("%s: %s", label, state)
	}
	return fmt.Sprintf("%s: %s (%s)", label, state, detail)
}
