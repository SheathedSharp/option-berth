package daemon

import (
	"errors"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// scanFailure renders a failed observation without flattening it into
// "internal". The collector already decided whether it saw nothing
// (observation_empty), saw a table it cannot trust (observation_incomplete),
// or hit an engine bug; the RPC keeps that decision and its hint for the agent
// on the other side of a sandbox.
func scanFailure(err error) *rpc.Error {
	var oe *ports.ObservationError
	if errors.As(err, &oe) {
		switch oe.Code {
		case ports.CodeObservationEmpty:
			return rpc.NewError(rpc.CodeObservationEmpty, oe.Error(), oe.Hint)
		case ports.CodeObservationIncomplete:
			return rpc.NewError(rpc.CodeObservationIncomplete, oe.Error(), oe.Hint)
		}
	}
	return rpc.NewError(rpc.CodeInternal, "scan failed: "+err.Error(),
		"check `oberth daemon log` for the scanner error")
}
