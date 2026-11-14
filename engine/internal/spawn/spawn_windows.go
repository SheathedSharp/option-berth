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

// jobs maps a child pid to the Job Object it was assigned to. Terminating the
// job takes the child and every descendant with it, which is Windows'
// equivalent of signalling a Unix process group.
var jobs sync.Map // pid -> windows.Handle

// configure gives the child a console process group of its own, so a Ctrl+C can
// be delivered to it and nothing else. A detached run also gets no console at
// all, which is what lets it survive its starter.
func configure(command *exec.Cmd, detach bool) {
	flags := uint32(windows.CREATE_NEW_PROCESS_GROUP)
	if detach {
		flags |= windows.DETACHED_PROCESS
	}
	command.SysProcAttr = &windows.SysProcAttr{CreationFlags: flags}
}

// adopt puts the freshly started child into a Job Object of its own.
//
// An attached run's job is killed when its last handle closes, so an
// `oberth start` that exits takes the tree with it. A detached run's job has no
// such limit and outlives the runtime service that created it.
func adopt(command *exec.Cmd, detach bool) error {
	if command.Process == nil {
		return nil
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("creating a job object: %w", err)
	}
	if detach {
		return attachToJob(job, command.Process.Pid)
	}
	if err := killWithTheLastHandle(job); err != nil {
		windows.CloseHandle(job)
		return err
	}
	return attachToJob(job, command.Process.Pid)
}

// killWithTheLastHandle asks Windows to terminate the job once no handle to it
// is left open, which ties the job's lifetime to its creator's.
func killWithTheLastHandle(job windows.Handle) error {
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return fmt.Errorf("configuring the job object: %w", err)
	}
	return nil
}

// attachToJob opens the child and assigns it to job, recording the pairing so
// killGroup can find it again. The job handle is deliberately kept open: that
// open handle is what keeps the job — and its kill-on-close promise — alive.
func attachToJob(job windows.Handle, pid int) error {
	child, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("opening the child process: %w", err)
	}
	defer windows.CloseHandle(child)
	if err := windows.AssignProcessToJobObject(job, child); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("assigning the child to the job object: %w", err)
	}
	jobs.Store(pid, job)
	return nil
}

// signalGroup delivers an interrupt as a console Ctrl+Break to the child's
// process group. Anything else — and a Ctrl+Break Windows refused to deliver —
// terminates the job instead.
func signalGroup(process *os.Process, sig os.Signal) error {
	if sig == os.Interrupt {
		if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(process.Pid)); err == nil {
			return nil
		}
	}
	return killGroup(process)
}

// killGroup terminates the child's Job Object, and every descendant with it.
func killGroup(process *os.Process) error {
	value, ok := jobs.Load(process.Pid)
	if !ok {
		return process.Kill()
	}
	if err := windows.TerminateJobObject(value.(windows.Handle), 1); err != nil {
		return fmt.Errorf("terminating the job object: %w", err)
	}
	return nil
}

// signalExitCode is unreachable in practice: Windows reports exit codes, not
// signals, so the 128+N translation has nothing to translate.
func signalExitCode(*exec.ExitError) int { return 1 }
