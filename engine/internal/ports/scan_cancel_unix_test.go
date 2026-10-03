//go:build linux || darwin

package ports

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestScanContextCancelsAndReapsListenerCommand(t *testing.T) {
	dir := t.TempDir()
	name := "ss"
	if runtime.GOOS == "darwin" {
		name = "netstat"
	}
	ready := filepath.Join(dir, "ready")
	// exec keeps the marker PID identical to the child CommandContext owns.
	script := "#!/bin/sh\nprintf '%s' \"$$\" > \"$OBERTH_SCAN_READY\"\nexec /bin/sleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("OBERTH_SCAN_READY", ready)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := ScanContext(ctx); done <- err }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(12 * time.Second):
				t.Error("owned command did not join during cleanup")
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
		t.Fatalf("listener command did not report PID: %q: %v", data, err)
	}
	cancel()
	select {
	case err := <-done:
		joined = true
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listener command ignored parent cancellation")
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("owned command %d not reaped: %v", pid, err)
	}
}
