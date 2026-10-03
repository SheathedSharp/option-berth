package ports

import (
	"context"
	"os/exec"
	"time"
)

// CommandTimeout bounds one system command the collector runs: `lsof`, `ss`,
// `netstat`, `ps`, `tasklist` and the Windows CIM query.
//
// The daemon's scan goroutine has nobody to notice a wedged command and no
// second chance to scan: a hung `lsof` on a dead mount would freeze state
// publication for every subscriber until the process happened to return. Ten
// seconds is far longer than any healthy invocation on this machine (tens of
// milliseconds) and short enough that a wedged command degrades to a failed
// collection — the last good snapshot stays published — instead of a loop
// that never ticks again. The docker CLI paths already work this way
// (docker.CLITimeout).
const CommandTimeout = 10 * time.Second

// commandTimeout is the value actually used, so tests do not have to wait out
// the real one to see a timeout reported.
var commandTimeout = CommandTimeout

// commandWaitDelay bounds how long a killed command may keep its output pipe
// open. Killing the direct child leaves anything it forked holding the pipe,
// and Output waits for the pipe, not the process (the same lesson as
// internal/userenv). One second is enough for a normal process tree to die
// and short enough not to eat the timeout's meaning.
const commandWaitDelay = time.Second

// bounded builds a system command that cannot outlive commandTimeout. The
// returned stop must be called once the output has been read.
func bounded(name string, args ...string) (*exec.Cmd, func()) {
	return boundedContext(context.Background(), name, args...)
}

// boundedContext keeps the per-command ceiling while honoring the caller's
// earlier cancellation or deadline. It does not create a detached worker.
func boundedContext(parent context.Context, name string, args ...string) (*exec.Cmd, func()) {
	ctx, cancel := context.WithTimeout(parent, commandTimeout)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = commandWaitDelay
	return cmd, cancel
}
