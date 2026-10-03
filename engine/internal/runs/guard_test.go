package runs

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTransactionGuardHelper(t *testing.T) {
	path := os.Getenv("BERTH_GUARD_HELPER_PATH")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := tryGuard(f); err != nil {
		t.Fatal(err)
	}
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
	releaseGuard(f)
}

func TestTransactionGuardCrashRelease(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	path := lockPath() + ".guard"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTransactionGuardHelper$")
	cmd.Env = append(os.Environ(), "BERTH_GUARD_HELPER_PATH="+path)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	reader := bufio.NewScanner(output)
	if !reader.Scan() || reader.Text() != "locked" {
		t.Fatal("helper did not lock")
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := tryGuard(f); err != errGuardBusy {
		t.Fatalf("live holder not exclusive: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if err := tryGuard(f); err != nil {
		t.Fatalf("crash did not release kernel guard: %v", err)
	}
	releaseGuard(f)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := withLock(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("transaction replaced its guard inode")
	}
}

func TestConcurrentStaleReclaimersRemainExclusive(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	// All contenders see the same dead legacy owner before racing to enter.
	if err := os.WriteFile(lockPath(), []byte("999999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lockPath(), old, old); err != nil {
		t.Fatal(err)
	}
	var active, entered atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	errors := make(chan error, 24)
	for i := 0; i < cap(errors); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errors <- withLock(func() error {
				if active.Add(1) != 1 {
					active.Add(-1)
					return fmt.Errorf("overlapping stale reclaimers")
				}
				defer active.Add(-1)
				time.Sleep(time.Millisecond)
				entered.Add(1)
				return nil
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if entered.Load() != 24 {
		t.Fatalf("entered=%d", entered.Load())
	}
}
