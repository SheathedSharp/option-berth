//go:build !windows

package groupstart

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/daemon/runsreg"
)

// TestPrepareCancellationKillsThePrepareProcess: a `prepare` that hangs must
// not outlive the `up` that started it. Canceling the stream kills the
// prepare's whole process group, ends the start, and leaves no run behind —
// a canceled prepare is not a service.
func TestPrepareCancellationKillsThePrepareProcess(t *testing.T) {
	dir := isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := freePort(t)
	pidFile := filepath.Join(dir, "prepare.pid")
	path := writeConfig(t, dir, fmt.Sprintf(`name: prepare-cancel
services:
  - name: api
    prepare: /bin/sh -c 'echo $$ > %s; sleep 30'
    cmd: %s
    port: %d
`, pidFile, serviceCmd(t), port))
	c := startDaemon(t, ctx, dir, []int{port})

	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{ConfigPath: &path}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}

	// Wait for the prepare shell to announce its pid.
	var pid int
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if b, err := os.ReadFile(pidFile); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && p > 0 {
				pid = p
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the prepare process never started")
	}

	// The stream API's cancellation path: `Stream.Cancel` asks the daemon to
	// stop the produce loop (the CLI's Ctrl-C closes the connection, which
	// tears the stream down the same way server-side).
	if err := s.Cancel(context.Background()); err != nil {
		t.Fatalf("cancel the stream: %v", err)
	}

	select {
	case <-s.End():
	case <-time.After(5 * time.Second):
		t.Fatal("canceling the stream did not end groups.start")
	}

	// The kill is synchronous inside prepare's cancellation branch; a short
	// grace absorbs the scheduler.
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if syscall.Kill(pid, 0) != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("prepare process %d survived cancellation", pid)
	}
	if runs := runsreg.Default.GroupRuns("prepare-cancel"); len(runs) != 0 {
		t.Fatalf("a canceled prepare registered runs: %+v", runs)
	}
}
