package daemon

import (
	"errors"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// observationRPCCodes keeps the collector's reason string and the wire number
// in one place. A new reason must be added here or it degrades to internal.
var observationRPCCodes = map[string]int{
	ports.CodeObservationEmpty:        rpc.CodeObservationEmpty,
	ports.CodeObservationUnrecognized: rpc.CodeObservationUnrecognized,
	ports.CodeObservationIncomplete:   rpc.CodeObservationIncomplete,
	ports.CodeObservationBlind:        rpc.CodeObservationBlind,
	ports.CodeObservationBindDenied:   rpc.CodeObservationBindDenied,
}

// scanFailure renders a failed observation without flattening it into
// "internal". The collector already decided whether it saw nothing
// (observation_empty), saw a table it cannot trust (observation_incomplete),
// or hit an engine bug; the RPC keeps that decision and its hint for the agent
// on the other side of a sandbox.
func scanFailure(err error) *rpc.Error {
	var oe *ports.ObservationError
	if errors.As(err, &oe) {
		if code, ok := observationRPCCodes[oe.Code]; ok {
			return rpc.NewError(code, oe.Error(), oe.Hint)
		}
	}
	return rpc.NewError(rpc.CodeInternal, "scan failed: "+err.Error(),
		"check `oberth daemon log` for the scanner error")
}
