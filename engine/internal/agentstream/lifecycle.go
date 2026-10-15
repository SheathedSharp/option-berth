package agentstream

import (
	"os/exec"
	"time"
)

const (
	interruptWait = 10 * time.Second
	stdinCloseWait = 5 * time.Second
	termSignalWait = 5 * time.Second
	killSignalWait = 5 * time.Second
)

func closeChildStdin(in interface{ Close() error }) {
	if in != nil {
		_ = in.Close()
	}
}

// reapChild waits for exit after stdin is closed, then escalates SIGTERM and SIGKILL to the process group.
func reapChild(cmd *exec.Cmd) int {
	if cmd == nil || cmd.Process == nil {
		return 0
	}
	exitCode := 1
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	wait := func(d time.Duration) bool {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-done:
			return true
		case <-timer.C:
			return false
		}
	}
	if !wait(stdinCloseWait) {
		_ = killProcessGroup(cmd)
	}
	if !wait(termSignalWait) {
		_ = killProcessGroupForce(cmd)
		if !wait(killSignalWait) {
			return exitCode
		}
	}
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode()
	}
	return 0
}
