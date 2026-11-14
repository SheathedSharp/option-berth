//go:build !windows

package killer

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

// signalOf maps the force flag onto the signal actually sent.
func signalOf(hard bool) syscall.Signal {
	switch {
	case hard:
		return syscall.SIGKILL
	default:
		return syscall.SIGTERM
	}
}

// signalProcess signals a single pid.
func signalProcess(pid int, hard bool) error {
	// PID 0 is the caller's own process group on unix, and a negative pid is a
	// process group. A failed scan must never turn into "signal everything we
	// are part of", so anything that is not a plain positive pid is refused.
	if !validSignalPID(pid) {
		return codedf(CodeInvalidSelector, "", "refusing to signal invalid PID %d", pid)
	}
	if err := syscall.Kill(pid, signalOf(hard)); err != nil {
		return mapSignalErr(pid, err)
	}
	return nil
}

// signalGroup signals every member of a process group.
func signalGroup(pgid int, hard bool) error {
	if !validSignalPID(pgid) || pgid <= 1 {
		return codedf(CodeInvalidSelector, "", "refusing to signal invalid process group %d", pgid)
	}
	if err := syscall.Kill(-pgid, signalOf(hard)); err != nil {
		return mapSignalErr(pgid, err)
	}
	return nil
}

// signalTree is the Windows entry point for whole-tree termination. On unix the
// engine walks the PPID table itself (children before parents), so this is only
// the single-process fallback.
func signalTree(pid int, hard bool) error { return signalProcess(pid, hard) }

// hasNativeTreeKill is false on Unix: the engine orders and signals the tree
// itself.
func hasNativeTreeKill() bool { return false }

// processGroup returns the group led by pid when pid is a leader and the group
// is not our own. Signalling a group we belong to would kill option-berth (and
// a shell job it came from), so a shared group reports unavailable and the
// caller walks the tree instead.
func processGroup(pid int) (int, bool) {
	if !validSignalPID(pid) || pid <= 1 {
		return 0, false
	}
	group, err := syscall.Getpgid(pid)
	if err != nil || group <= 1 {
		return 0, false
	}
	// Only a leader's group is safe to signal wholesale: exactly the process
	// and its descendants spawned with Setpgid.
	if group != pid {
		return 0, false
	}
	if current, err := syscall.Getpgid(0); err == nil && current == group {
		return 0, false
	}
	return group, true
}

// pidAlive reports whether the process still exists. Signal 0 checks for
// existence without delivering anything; EPERM means it exists but belongs to
// someone else.
func pidAlive(pid int) bool {
	if !validSignalPID(pid) {
		return false
	}
	sendErr := syscall.Kill(pid, 0)
	return sendErr == nil || errors.Is(sendErr, syscall.EPERM)
}

// SetProcessGroup puts a child in its own process group so that the whole tree
// it spawns can later be stopped with one signal. Spawners (`oberth start`, the
// runtime service's runs.spawn) call this; the killer then finds the group through
// processGroup and signals it instead of walking the PPID table.
func SetProcessGroup(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.Setpgid = true
}

// mapSignalErr turns a kill(2) errno into the contract's error model.
func mapSignalErr(target int, signalErr error) error {
	switch {
	case errors.Is(signalErr, syscall.ESRCH):
		return codedf(CodeNotFound, "", "PID %d is already gone", target)
	case errors.Is(signalErr, syscall.EPERM):
		return permissionErr(target, signalErr)
	}
	return fmt.Errorf("signalling PID %d: %w", target, signalErr)
}

// Unix kill/getpgid accept a signed pid_t even when Go int is 64-bit. Reject
// truncation: e.g. 0xffffffff must never turn into kill(-1, signal).
func validSignalPID(pid int) bool { return pid > 0 && uint64(pid) < 1<<31 }
