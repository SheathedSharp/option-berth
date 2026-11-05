//go:build windows

package killer

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// Windows has no signals: `taskkill` without /F posts WM_CLOSE or CTRL_BREAK
// (the polite stop), /F terminates outright (the hard stop), and /T adds the
// whole process tree — the Job Object equivalent without x/sys/windows.
func taskkill(args ...string) error {
	return taskkillContext(context.Background(), args...)
}

func taskkillContext(ctx context.Context, args ...string) error {
	command, cancel := controlCommand(ctx, "taskkill", args...)
	defer cancel()
	output, runErr := command.CombinedOutput()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if runErr == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	folded := strings.ToLower(message)
	switch {
	case strings.Contains(folded, "not found"):
		return codedf(CodeNotFound, "", "%s", message)
	case strings.Contains(folded, "access is denied"), strings.Contains(folded, "denied"):
		return &CodedError{
			Code:   CodePermissionDenied,
			Detail: message,
			Hint:   "re-run from an elevated prompt",
			Err:    runErr,
		}
	}
	return codedf(CodeInternal, "", "%s", message)
}

func killArgs(pid int, hard, tree bool) []string {
	command := []string{"/PID", strconv.Itoa(pid)}
	if tree {
		command = append(command, "/T")
	}
	if hard {
		command = append([]string{"/F"}, command...)
	}
	return command
}

// signalProcess terminates a single process.
func signalProcess(pid int, hard bool) error {
	if !validSignalPID(pid) {
		return codedf(CodeInvalidSelector, "", "refusing to signal invalid PID %d", pid)
	}
	return taskkill(killArgs(pid, hard, false)...)
}

// signalTree terminates a process and everything below it in one call.
func signalTree(pid int, hard bool) error {
	if !validSignalPID(pid) {
		return codedf(CodeInvalidSelector, "", "refusing to signal invalid PID %d", pid)
	}
	return taskkill(killArgs(pid, hard, true)...)
}

// signalGroup is unreachable on Windows: processGroup never reports a group.
func signalGroup(pgid int, hard bool) error { return signalTree(pgid, hard) }

// hasNativeTreeKill is true on Windows: taskkill terminates a tree itself, so
// each target yields one row instead of a PPID walk.
func hasNativeTreeKill() bool { return true }

// processGroup has no safe Windows equivalent without a Job Object handle held
// from spawn time; tree termination uses taskkill /T.
func processGroup(pid int) (int, bool) { return 0, false }

// pidAlive uses a process handle, not a tasklist subprocess. Denied access or
// a failed query is not proof of exit; retain conservative liveness.
func pidAlive(pid int) bool {
	if !validSignalPID(pid) {
		return false
	}
	// A zero-time wait avoids confusing an actual exit code 259 with STILL_ACTIVE.
	const synchronize = 0x00100000
	handle, openErr := syscall.OpenProcess(synchronize, false, uint32(pid))
	if openErr != nil {
		return errors.Is(openErr, syscall.ERROR_ACCESS_DENIED)
	}
	defer syscall.CloseHandle(handle)
	wait, waitErr := syscall.WaitForSingleObject(handle, 0)
	// Only a signalled process handle proves exit; uncertainty stays alive.
	return waitErr != nil || wait != syscall.WAIT_OBJECT_0
}

// SetProcessGroup gives a child its own console process group so that a later
// taskkill /T stops the whole tree and a Ctrl+Break aimed at it does not travel
// back to option-berth.
func SetProcessGroup(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

func validSignalPID(pid int) bool { return pid > 0 && uint64(pid) < 1<<32 }
