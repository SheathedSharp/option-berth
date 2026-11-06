package doctor

import (
	"context"
	"errors"
	"fmt"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
)

// checkDocker is about enrichment, never about health: option-berth works fine with no
// docker on the machine. The one thing worth reporting is the confusing case —
// the CLI is installed but its daemon is not answering, so container names and
// images are silently missing from every listing.
func checkDocker(ctx context.Context, env *Env) rpc.DoctorCheck {
	path, err := env.LookPath("docker")
	if err != nil {
		return skip("docker is not installed",
			"container names, images and compose projects are not enriched")
	}
	version, err := env.Docker(ctx)
	if errors.Is(err, ErrDockerNotAnswering) {
		return warn("docker CLI not answering", fmt.Sprintf("%s: %v", path, err),
			"Docker is running but wedged: restart Docker Desktop (or the docker service); "+
				"until then the daemon keeps the last container list it saw and retries with backoff")
	}
	if err != nil {
		return warn("docker is installed but not responding", fmt.Sprintf("%s: %v", path, err),
			"start Docker Desktop (or the docker service); until then container rows are unenriched")
	}
	return ok("docker is responding", fmt.Sprintf("%s, server %s", path, version))
}
