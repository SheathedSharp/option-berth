package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// checkListenerObservation answers "can this process see its own listeners?".
// It binds a real local port and asks the same collector a status read uses
// whether that listener is visible as this process. In CLI mode it also reports
// the daemon's own probe: a healthy CLI cannot make a blind daemon's empty port
// list trustworthy.
func checkListenerObservation(ctx context.Context, env *Env) rpc.DoctorCheck {
	ctx, cancel := context.WithTimeout(ctx, NetworkBudget)
	defer cancel()

	cli := env.ProbeObservation(ctx)
	parts := []string{observationPart("cli", cli.State, cli.Detail)}
	fix := cli.Hint

	var daemon *rpc.ObservationStatus
	if env.Mode == ModeCLI {
		if info := env.Daemon(ctx); info.Reachable {
			daemon = info.Observation
			if daemon == nil {
				parts = append(parts, "daemon: observation not reported")
			} else {
				parts = append(parts, observationPart("daemon", observationState(daemon.State), daemon.Detail))
			}
		}
	}

	detail := strings.Join(parts, "; ")
	if cli.State == ports.ObservationBindDenied {
		return rpc.DoctorCheck{
			Status:  StatusFail,
			Summary: "this process cannot bind a local port",
			Detail:  detail,
			Fix:     fix,
		}
	}
	if cli.State != ports.ObservationObservable {
		return rpc.DoctorCheck{
			Status:  StatusFail,
			Summary: "this process cannot see its own listener",
			Detail:  detail,
			Fix:     fix,
		}
	}
	if daemon != nil && daemon.State != string(ports.ObservationObservable) {
		if daemon.Hint != "" {
			fix = daemon.Hint
		}
		return rpc.DoctorCheck{
			Status:  StatusFail,
			Summary: "the daemon cannot see its own listeners",
			Detail:  detail,
			Fix:     fix,
		}
	}
	return rpc.DoctorCheck{
		Status:  StatusOK,
		Summary: "this process can see its own listener",
		Detail:  detail,
	}
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
