//go:build !windows

package spawn

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configure gives the child a process group of its own, which is what makes a
// whole dev-server tree signalable as one unit. A detached run additionally
// gets its own session, so it outlives the terminal that started it; Setsid
// already implies a new process group, which is why the two are exclusive.
func configure(command *exec.Cmd, detach bool) {
	attributes := &syscall.SysProcAttr{}
	if detach {
		attributes.Setsid = true
	} else {
		attributes.Setpgid = true
	}
	command.SysProcAttr = attributes
}

// adopt is a no-op here: the process group is settled at fork time, so there is
// nothing to hand over once the child is running.
func adopt(*exec.Cmd, bool) error { return nil }

// signalGroup sends sig to the child's whole process group. It falls back to
// the process itself when the group cannot be read — a child that called
// setsid, say — and treats an already-gone group as delivered.
func signalGroup(process *os.Process, sig os.Signal) error {
	unixSignal, ok := sig.(syscall.Signal)
	if !ok {
		return errors.New("spawn: unsupported signal")
	}
	group, err := syscall.Getpgid(process.Pid)
	if err != nil {
		group = process.Pid
	}
	if err := syscall.Kill(-group, unixSignal); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return process.Signal(sig)
	}
	return nil
}

// killGroup SIGKILLs the child's process group.
func killGroup(process *os.Process) error { return signalGroup(process, syscall.SIGKILL) }

// signalExitCode turns "killed by SIGINT" into the shell's 130, so a Ctrl+C
// through `oberth start` looks the same as running the command directly.
func signalExitCode(err *exec.ExitError) int {
	status, ok := err.Sys().(syscall.WaitStatus)
	if ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return 1
}
