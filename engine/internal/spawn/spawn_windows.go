//go:build windows

package spawn

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobs maps a child pid to its Job Object. Terminating the job stops the child
// and every descendant — Windows' equivalent of signalling a Unix group.
var jobs sync.Map // pid -> windows.Handle

// configure gives the child its own console process group so Ctrl+C can be
// delivered to it (and only it) with GenerateConsoleCtrlEvent. A detached run
// additionally gets no console at all, so it survives its starter.
func configure(command *exec.Cmd, detached bool) {
	flags := uint32(windows.CREATE_NEW_PROCESS_GROUP)
	if detached {
		flags |= windows.DETACHED_PROCESS
	}
	command.SysProcAttr = &windows.SysProcAttr{CreationFlags: flags}
}

// adopt puts the started child into a fresh Job Object. An attached run's job
// dies with its handle, so a `oberth start` that exits takes the tree with it; a
// detached run's job has no such limit and outlives the daemon that made it.
func adopt(command *exec.Cmd, detached bool) error {
	if command.Process == nil {
		return nil
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("creating a job object: %w", err)
	}
	if !detached {
		var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(job,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
			windows.CloseHandle(job)
			return fmt.Errorf("configuring the job object: %w", err)
		}
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false, uint32(command.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("opening the child process: %w", err)
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("assigning the child to the job object: %w", err)
	}
	jobs.Store(command.Process.Pid, job)
	return nil
}

// signalGroup forwards an interrupt as a console Ctrl+Break to the child's
// process group; anything else terminates the job.
func signalGroup(process *os.Process, signal os.Signal) error {
	if signal == os.Interrupt {
		if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(process.Pid)); err == nil {
			return nil
		}
	}
	return killGroup(process)
}

// killGroup terminates the child's Job Object, and with it every descendant.
func killGroup(process *os.Process) error {
	if stored, ok := jobs.Load(process.Pid); ok {
		job := stored.(windows.Handle)
		if err := windows.TerminateJobObject(job, 1); err != nil {
			return fmt.Errorf("terminating the job object: %w", err)
		}
		return nil
	}
	return process.Kill()
}

// signalExitCode is the fallback for an exit status Windows reports without a
// code; there are no signals to translate.
func signalExitCode(*exec.ExitError) int { return 1 }
