package doctor

import (
	"context"
	"fmt"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"strings"
)

func checkDaemonBuildMatches(ctx context.Context, env *Env) rpc.DoctorCheck {
	info := env.Daemon(ctx)
	switch {
	case !info.Reachable:
		return skip("no daemon build to compare", "")
	case info.StatusErr != nil:
		return warn("daemon build could not be observed", info.StatusErr.Error(), "oberth daemon status --json")
	case !knownCommit(env.Commit) || !knownCommit(info.Commit):
		return warn("build identity is unknown",
			"a missing or development commit cannot confirm a build match",
			"oberth version --json; oberth daemon status --json")
	case env.Commit != info.Commit:
		return warn("daemon and CLI come from different builds",
			fmt.Sprintf("daemon commit %s; CLI commit %s", info.Commit, env.Commit),
			"oberth daemon restart")
	}
	return ok("daemon and CLI build identities match", "commit "+env.Commit)
}

func knownCommit(commit string) bool {
	switch strings.ToLower(strings.TrimSpace(commit)) {
	case "", "unknown", "local", "dev", "devel", "(devel)":
		return false
	default:
		return true
	}
}
