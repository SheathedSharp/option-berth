package docker

import (
	"context"
	"os/exec"
	"time"
)

// CLITimeout bounds one `docker` invocation.
//
// The docker CLI has no timeout of its own: it talks to a runtime service over a socket,
// and when Docker Desktop is starting, paused or wedged, `docker stats` does
// not fail — it waits. Ten seconds is well beyond a healthy
// `docker stats --no-stream` (about two) and short enough that a wedged runtime service
// degrades to "no container data" rather than to a hung command.
//
// Scans no longer pay this cost; they read the container list a Watcher keeps
// in the background. It still bounds the one-shot CLI paths and explicit
// actions such as `docker stop`.
const CLITimeout = 10 * time.Second

// waitDelay bounds how long a killed `docker` may hold its output pipe open.
// Killing the process is not enough on its own: anything it started that
// inherited the pipe would keep Output waiting past the deadline.
const waitDelay = time.Second

// timedCommand is a `docker` command that cannot outlive CLITimeout. The
// returned stop function releases the timer and must run once the output has
// been read.
func timedCommand(args ...string) (*exec.Cmd, func()) {
	ctx, release := context.WithTimeout(context.Background(), CLITimeout)
	return dockerCommand(ctx, args...), release
}

// dockerCommand builds one `docker` invocation bound to ctx, with the pipe
// delay applied so a killed child cannot strand its readers.
func dockerCommand(ctx context.Context, args ...string) *exec.Cmd {
	invocation := exec.CommandContext(ctx, "docker", args...)
	invocation.WaitDelay = waitDelay
	return invocation
}

// dockerOutput is one `docker` call under CLITimeout, returning its stdout.
func dockerOutput(args ...string) ([]byte, error) {
	invocation, release := timedCommand(args...)
	defer release()
	return invocation.Output()
}
