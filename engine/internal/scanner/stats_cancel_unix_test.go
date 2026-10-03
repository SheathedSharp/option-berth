//go:build !windows

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
)

// The default sampler, not a mock: replace only its native ps executable with
// an owned process that announces entry and then cannot finish without cancel.
func TestNativeStatsCommandStopsWithSamplerContext(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "entered")
	script := "#!/bin/sh\nprintf '%s' \"$$\" > \"$OBERTH_STATS_READY\"\nexec /bin/sleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "ps"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("OBERTH_STATS_READY", ready)
	l := New(Options{Demand: func() (int, Include) { return 1, Include{Stats: true} }, Scan: func(Include) ([]ports.ListeningPort, error) {
		return []ports.ListeningPort{{Port: 8080, PID: 42, MemoryRSS: 100}}, nil
	}})
	l.scanAndPublish(Include{Stats: true})
	before := l.Cached()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); l.runStats(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(12 * time.Second):
			t.Error("native sampler did not exit")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(ready)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		if pid > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("native command did not reach barrier")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sampler cancellation did not interrupt native command")
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("owned command remains after cancellation: pid=%d err=%v", pid, err)
	}
	after := l.Cached()
	if after.Seq != before.Seq || after.Ports[0].Stats.MemoryRSS != 100 {
		t.Fatal("cancelled sample published replacement facts")
	}
}
