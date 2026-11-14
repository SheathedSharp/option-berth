// Package restartout preserves the CLI's stop/start receipts across transport errors.
// It performs no I/O itself; the CLI supplies its existing output adapters.
package restartout

import (
	"context"
	"errors"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/runstate"
)

type Start struct {
	Initial rpc.GroupsStartResult
	Chunks  []rpc.GroupsStartChunk
	End     rpc.GroupsStartEnd
}

// Document is the existing restart JSON shape. An absent terminal start summary
// stays unknown (null), rather than fabricating success from partial receipts.
type Document struct {
	Stopped  rpc.KillEnvelope       `json:"stopped"`
	Services []rpc.GroupsStartChunk `json:"services"`
	StartID  string                 `json:"start_id,omitempty"`
	rpc.GroupsStartEnd
}

// Execute owns sequencing and hands the actual receipts to the output adapter.
// complete means no operation error interrupted the sequence; it is not readiness.
// A stop refusal has no start summary. The original error still belongs to stderr.
func Execute(ctx context.Context, stop func(context.Context) (rpc.KillEnvelope, error), start func(context.Context) (Start, error), emit func(Document, bool) error) error {
	knownStop := false
	stopped, started, err := runstate.Restart(ctx,
		func(ctx context.Context) (rpc.KillEnvelope, bool, error) {
			result, err := stop(ctx)
			knownStop = err == nil || result.OK || len(result.Results) > 0
			return result, result.OK, err
		}, start)
	if !knownStop {
		return err
	}
	chunks := started.Chunks
	if chunks == nil {
		chunks = []rpc.GroupsStartChunk{}
	}
	doc := Document{Stopped: stopped, Services: chunks, StartID: started.Initial.StartID, GroupsStartEnd: started.End}
	if outputErr := emit(doc, err == nil); outputErr != nil {
		if err != nil {
			return errors.Join(err, outputErr)
		}
		return outputErr
	}
	return err
}
