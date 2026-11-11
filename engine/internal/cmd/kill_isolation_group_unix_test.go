//go:build !windows

package cmd

import (
	"fmt"
	"syscall"
)

// killHeldProcessGroupDefault signals the process group this fixture created,
// through the held leader. Windows has no Setpgid-created group; its variant
// refuses instead of inventing one.
func killHeldProcessGroupDefault(id isolatedProcessIdentity) error {
	if id.Handle == nil || id.Handle.Pid <= 0 || id.Handle.Pid != id.PID {
		return fmt.Errorf("%w: held process group is not this identity", errUnscopedKillCleanup)
	}
	err := syscall.Kill(-id.Handle.Pid, syscall.SIGKILL)
	if err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}
