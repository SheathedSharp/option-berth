package doctor

import (
	"context"
	"fmt"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"strings"
)

func checkRuntimeServiceBuildMatches(ctx context.Context, env *Env) rpc.DoctorCheck {
	info := env.RuntimeService(ctx)
	switch {
	case !info.Reachable:
		return skip("no runtime service build to compare", "")
	case info.StatusErr != nil:
		return warn("runtime service build could not be observed", info.StatusErr.Error(), "oberth runtime service status --json")
	case !knownCommit(env.Commit) || !knownCommit(info.Commit):
		return warn("build identity is unknown",
			"a missing or development commit cannot confirm a build match",
			"oberth version --json; oberth runtime service status --json")
	case env.Commit != info.Commit:
		return warn("runtime service and CLI come from different builds",
			fmt.Sprintf("runtime service commit %s; CLI commit %s", info.Commit, env.Commit),
			"oberth runtime service restart")
	}
	return ok("runtime service and CLI build identities match", "commit "+env.Commit)
}

func knownCommit(commit string) bool {
	switch strings.ToLower(strings.TrimSpace(commit)) {
	case "", "unknown", "local", "dev", "devel", "(devel)":
		return false
	default:
		return true
	}
}
