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

// ErrDockerNotAnswering marks the wedged-backend case: `docker info` was
// accepted but did not answer in time. It is a different repair from a Docker
// that is simply not running.
var ErrDockerNotAnswering = errors.New("docker CLI not answering")

// DockerProbe asks the local docker daemon for its server version, within
// NetworkBudget. A failed or empty answer means the CLI is installed while its
// daemon is not answering — the state that quietly strips container names off
// every listing.
func DockerProbe(ctx context.Context) (string, error) {
	ctx, stop := context.WithTimeout(ctx, NetworkBudget)
	defer stop()

	command := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}")
	command.WaitDelay = time.Second
	output, err := command.Output()
	switch {
	case err == nil:
		return dockerServerVersion(output)
	case ctx.Err() == context.DeadlineExceeded:
		return "", fmt.Errorf("%w: `docker info` did not answer within %s", ErrDockerNotAnswering, NetworkBudget)
	}
	return "", dockerCommandError(err)
}

// dockerServerVersion validates what `docker info` printed. An empty answer is
// a failure, not a version.
func dockerServerVersion(output []byte) (string, error) {
	version := strings.TrimSpace(string(output))
	if version == "" {
		return "", errors.New("`docker info` reported no server version")
	}
	return version, nil
}

// dockerCommandError keeps the CLI's own first stderr line, which names the
// real problem; anything else is returned unchanged.
func dockerCommandError(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return errors.New(firstLine(string(exit.Stderr)))
	}
	return err
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
