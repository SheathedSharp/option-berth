//go:build !windows

package cmd

import (
	"github.com/sheathedsharp/option-berth/internal/agentlaunch"
	"os"
	"syscall"
)

// Exec retains the caller's actual controlling terminal and the external agent's
// own lifecycle. No shell, command inference, or second supervisor is inserted.
func executeAgent(plan agentlaunch.Plan) error {
	old, err := os.Getwd()
	if err != nil {
		return err
	}
	if err = os.Chdir(plan.Worktree); err != nil {
		return err
	}
	err = syscall.Exec(plan.Executable, append([]string{plan.Executable}, plan.Arguments...), os.Environ())
	_ = os.Chdir(old)
	return err
}
