//go:build linux || darwin

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestDefaultRunCancelsAndReapsListenerCommand(t *testing.T) {
	dir := t.TempDir()
	name := "ss"
	if runtime.GOOS == "darwin" {
		name = "netstat"
	}
	ready := filepath.Join(dir, "ready")
	script := "#!/bin/sh\nprintf '%s' \"$$\" > \"$OBERTH_SCAN_READY\"\nexec /bin/sleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("OBERTH_SCAN_READY", ready)
	ctx, cancel := context.WithCancel(context.Background())
	publications := 0
	l := New(Options{Demand: func() (int, Include) { return 1, Include{} }, Publish: func(state.Snapshot, state.Snapshot, []state.Event) { publications++ }})
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(12 * time.Second):
				t.Error("default Run did not join during cleanup")
			}
		}
	}()
	var data []byte
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, _ = os.ReadFile(ready)
		if len(data) > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("collector did not report PID: %q %v", data, err)
	}
	cancel()
	select {
	case <-done:
		joined = true
	case <-time.After(2 * time.Second):
		t.Fatal("default scanner ignored owner cancellation")
	}
	if publications != 0 || l.Status().Seq != 0 || l.Status().LastError != nil {
		t.Fatalf("cancelled default Run published %d: %+v", publications, l.Status())
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("owned collector %d not reaped: %v", pid, err)
	}
}
