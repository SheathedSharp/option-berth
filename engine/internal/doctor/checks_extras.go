package doctor

import (
	"context"
	"errors"
	"fmt"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// checkDocker is about enrichment, never about health: option-berth runs fine
// with no docker on the machine. The one confusing state worth a row is a CLI
// that is installed while its daemon is not answering, because container names
// and images then go silently missing from every listing.
func checkDocker(ctx context.Context, env *Env) rpc.DoctorCheck {
	path, err := env.LookPath("docker")
	if err != nil {
		return skip("docker is not installed",
			"container names, images and compose projects are not enriched")
	}
	version, err := env.Docker(ctx)
	switch {
	case errors.Is(err, ErrDockerNotAnswering):
		return warn("docker CLI not answering", fmt.Sprintf("%s: %v", path, err),
			"Docker is running but wedged: restart Docker Desktop (or the docker service); "+
				"until then the daemon keeps the last container list it saw and retries with backoff")
	case err != nil:
		return warn("docker is installed but not responding", fmt.Sprintf("%s: %v", path, err),
			"start Docker Desktop (or the docker service); until then container rows are unenriched")
	}
	return ok("docker is responding", fmt.Sprintf("%s, server %s", path, version))
}
