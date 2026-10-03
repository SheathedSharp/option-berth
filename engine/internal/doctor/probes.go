package doctor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// NetworkBudget is how long the doctor waits on anything off this machine.
// A check that runs out of budget is a `skip`: an offline machine is not a
// broken one, and a diagnosis must not itself hang.
//
// Everything the doctor asks over this budget is a question about this machine
// (docker, above all). It never asks the network what the newest release is:
// this fork is built from source, so there is no feed to compare against.
const NetworkBudget = 2 * time.Second

// ErrDockerNotAnswering is DockerProbe's error when `docker info` hangs rather
// than failing: the backend is up but wedged, which is a different fix from a
// Docker that is simply not running.
var ErrDockerNotAnswering = errors.New("docker CLI not answering")

// DockerProbe asks the local docker daemon for its server version, within
// NetworkBudget. An error means the CLI is installed but its daemon is not
// answering — the state that quietly strips container names off every listing.
// A timeout wraps ErrDockerNotAnswering.
func DockerProbe(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, NetworkBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("%w: `docker info` did not answer within %s", ErrDockerNotAnswering, NetworkBudget)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", errors.New(firstLine(string(exit.Stderr)))
		}
		return "", err
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		return "", errors.New("`docker info` reported no server version")
	}
	return version, nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
