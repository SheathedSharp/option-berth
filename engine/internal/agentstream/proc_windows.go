//go:build windows

package agentstream

import "os/exec"

func setProcessGroup(cmd *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func killProcessGroupForce(cmd *exec.Cmd) error {
	return killProcessGroup(cmd)
}
