//go:build !windows

package cmd

import (
	"os/exec"
	"syscall"
)

// detachProcess puts the runtime service in a session of its own. That is what lets it
// outlive the shell that launched it and keeps the terminal's Ctrl+C from
// reaching it: a session leader has no controlling terminal to deliver to.
func detachProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
