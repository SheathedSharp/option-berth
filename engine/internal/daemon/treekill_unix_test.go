//go:build integration && !windows

package daemon_test

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup puts a command in a process group of its own so the test can
// signal it separately from the `go test` process it was started from.
func ownProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killTree SIGKILLs a command, everything below it in the process table and
// its process group. All three: a `oberth start` child gets a process group of
// its own, so the group kill alone would miss it, and the process table can be
// a scan behind.
//
// The signals are numeric-pid operations, so they are only safe while the
// command is unreaped: a zombie keeps its pid allocated, and a pid that has
// already been waited on can name an unrelated process. Once Wait has run there
// is nothing of this command left to stop.
func killTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		return
	}
	pid := cmd.Process.Pid
	for _, p := range descendants(pid) {
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid > 1 {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	_ = cmd.Process.Kill()
}
