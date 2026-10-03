package docker

import (
	"context"
	"fmt"

	"github.com/sheathedsharp/option-berth/internal/ports"
)

// EnrichPortsContext retains optional Docker enrichment on ordinary failure,
// but request cancellation cannot become a successful native-only observation.
func EnrichPortsContext(ctx context.Context, pp []ports.ListeningPort) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	round, cancel := context.WithTimeout(ctx, CLITimeout)
	defer cancel()
	containers, err := listContainersCtx(round)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		enrichFrom(pp, containers)
	}
	return ctx.Err()
}

func StopContainerContext(ctx context.Context, name string) error {
	round, cancel := context.WithTimeout(ctx, CLITimeout)
	defer cancel()
	err := command(round, "stop", name).Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("failed to stop container %s: %w", name, err)
	}
	return nil
}
