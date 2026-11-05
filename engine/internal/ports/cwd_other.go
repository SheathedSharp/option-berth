//go:build !linux && !darwin && !windows

package ports

import "context"

// These platforms expose no per-process working directory: Linux reads /proc,
// macOS asks lsof, Windows reads the PEB. Here Cwd simply stays empty, which
// makes resolveProcessName skip its cwd-augmentation step rather than guess.
func batchGetCwds(pids []int) map[int]string {
	return batchGetCwdsContext(context.Background(), pids)
}

// batchGetCwdsContext answers nothing, whatever the deadline or the pid list.
func batchGetCwdsContext(context.Context, []int) map[int]string {
	return nil
}
