//go:build linux

package ports

import (
	"context"
	"os"
	"strconv"
	"strings"
)

// cwdDeletedSuffix is what the kernel appends to a cwd link once the directory
// is gone. It is the kernel's note, not part of the path: the row carries
// cwd_gone for that fact, so the path itself stays clean.
const cwdDeletedSuffix = " (deleted)"

// batchGetCwds returns pid -> cwd by reading /proc/<pid>/cwd symlinks.
// No exec needed; this is essentially free.
func batchGetCwds(pids []int) map[int]string {
	return batchGetCwdsContext(context.Background(), pids)
}

func batchGetCwdsContext(ctx context.Context, pids []int) map[int]string {
	result := make(map[int]string)
	for _, pid := range pids {
		if ctx.Err() != nil {
			return nil
		}
		if cwd, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd"); err == nil {
			result[pid] = strings.TrimSuffix(cwd, cwdDeletedSuffix)
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return result
}
