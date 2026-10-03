//go:build darwin

package ports

import (
	"context"
	"strconv"
	"strings"
)

// batchGetCwds returns pid -> cwd for the given PIDs using a single lsof call.
// A non-zero exit is not a failure: see cwdsFromLsof.
func batchGetCwds(pids []int) map[int]string {
	return batchGetCwdsContext(context.Background(), pids)
}

func batchGetCwdsContext(ctx context.Context, pids []int) map[int]string {
	if len(pids) == 0 || ctx.Err() != nil {
		return map[int]string{}
	}
	pidStrs := make([]string, len(pids))
	for i, p := range pids {
		pidStrs[i] = strconv.Itoa(p)
	}
	cmd, stop := boundedContext(ctx, "lsof", "-a", "-p", strings.Join(pidStrs, ","), "-d", "cwd", "-Fpn")
	defer stop()
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil
	}
	return cwdsFromLsof(out, err)
}
