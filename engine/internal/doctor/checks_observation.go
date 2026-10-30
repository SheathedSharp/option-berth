package doctor

import (
	"context"
	"errors"
	"fmt"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// checkListenerObservation answers "can this process read the listening
// table?". It is the one check whose failure can make every other reading
// misleading: a sandboxed process that is allowed to run option-berth but not
// to inspect sockets would otherwise report an empty machine as healthy.
//
// It intentionally uses the same collector as `status` rather than a cheaper
// probe, so the check proves exactly the path that failed.
func checkListenerObservation(ctx context.Context, env *Env) rpc.DoctorCheck {
	ctx, cancel := context.WithTimeout(ctx, NetworkBudget)
	defer cancel()

	rows, err := env.ObserveListeners(ctx)
	if err == nil {
		return rpc.DoctorCheck{
			Status:  StatusOK,
			Summary: "this process can read the TCP listening table",
			Detail:  fmt.Sprintf("%d listener row(s) observed by the %s process", len(rows), env.Mode),
		}
	}

	var oe *ports.ObservationError
	if errors.As(err, &oe) {
		return rpc.DoctorCheck{
			Status:  StatusFail,
			Summary: observationSummary(oe.Code),
			Detail:  fmt.Sprintf("%s: %s", oe.Code, oe.Error()),
			Fix:     oe.Hint,
		}
	}
	return rpc.DoctorCheck{
		Status:  StatusFail,
		Summary: "this process cannot read the TCP listening table",
		Detail:  err.Error(),
		Fix: "retry in a normal terminal outside any agent sandbox; " +
			"if it still fails, check that the host's netstat/ss command runs",
	}
}

func observationSummary(code string) string {
	switch code {
	case ports.CodeObservationEmpty:
		return "the collector returned no listening table"
	case ports.CodeObservationIncomplete:
		return "the collector's output could not be trusted"
	default:
		return "listening observation failed"
	}
}
