//go:build windows

package cmd

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// detachProcess hands the runtime service a new process group and no console, for the
// same two reasons as on Unix: the launching shell may exit without taking the
// runtime service with it, and Ctrl+C must not reach it.
func detachProcess(command *exec.Cmd) {
	command.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}
