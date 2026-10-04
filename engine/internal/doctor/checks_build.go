package doctor

import (
	"context"
	"fmt"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"strings"
)

func checkDaemonBuildMatches(ctx context.Context, env *Env) rpc.DoctorCheck {
	info := env.Daemon(ctx)
	if !info.Reachable {
		return rpc.DoctorCheck{Status: StatusSkip, Summary: "no daemon build to compare"}
	}
	if info.StatusErr != nil {
		return rpc.DoctorCheck{Status: StatusWarn, Summary: "daemon build could not be observed", Detail: info.StatusErr.Error(), Fix: "oberth daemon status --json"}
	}
	if !knownCommit(env.Commit) || !knownCommit(info.Commit) {
		return rpc.DoctorCheck{Status: StatusWarn, Summary: "build identity is unknown", Detail: "a missing or development commit cannot confirm a build match", Fix: "oberth version --json; oberth daemon status --json"}
	}
	if env.Commit != info.Commit {
		return rpc.DoctorCheck{Status: StatusWarn, Summary: "daemon and CLI come from different builds", Detail: fmt.Sprintf("daemon commit %s; CLI commit %s", info.Commit, env.Commit), Fix: "oberth daemon restart"}
	}
	return rpc.DoctorCheck{Status: StatusOK, Summary: "daemon and CLI build identities match", Detail: "commit " + env.Commit}
}

func knownCommit(commit string) bool {
	switch strings.ToLower(strings.TrimSpace(commit)) {
	case "", "unknown", "local", "dev", "devel", "(devel)":
		return false
	default:
		return true
	}
}
