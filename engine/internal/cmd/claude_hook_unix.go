//go:build !windows

package cmd

import (
	"os/exec"
	"syscall"
)

func detachClaudeObserver(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}
