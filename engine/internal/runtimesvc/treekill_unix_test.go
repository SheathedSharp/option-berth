//go:build integration && !windows

package runtimesvc_test

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup gives a command a process group of its own, so the test can
// signal it without signalling the `go test` process that started it.
func ownProcessGroup(cmd *exec.Cmd) {
	attr := cmd.SysProcAttr
	if attr == nil {
		attr = &syscall.SysProcAttr{}
		cmd.SysProcAttr = attr
	}
	attr.Setpgid = true
}

// killTree SIGKILLs the command, every process the table reports below it, and
// its process group — all three, because a `oberth start` child is put in a
// process group of its own (the group kill alone would miss it) and the
// process table can trail the truth by one scan.
//
// Each of those is a numeric-pid operation, so they are only safe while the
// command is still unreaped: a zombie keeps its pid allocated, and a pid that
// has already been waited on can name an unrelated process. Once Wait has run,
// nothing of this command is left to signal.
func killTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		return
	}
	root := cmd.Process.Pid
	for _, pid := range descendants(root) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	if group, err := syscall.Getpgid(root); err == nil && group > 1 {
		_ = syscall.Kill(-group, syscall.SIGKILL)
	}
	_ = cmd.Process.Kill()
}
