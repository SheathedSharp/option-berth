package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/scanner"
)

func TestCheckedStartupFailureUnwindsBeforeServing(t *testing.T) {
	// Startup registration is process-global; this test is deliberately serial
	// and restores it after the failing server has finished.
	hooksMu.Lock()
	oldStart, oldShutdown := startHooks, shutdownHooks
	startHooks, shutdownHooks = nil, nil
	hooksMu.Unlock()
	t.Cleanup(func() {
		hooksMu.Lock()
		startHooks, shutdownHooks = oldStart, oldShutdown
		hooksMu.Unlock()
	})
	// t.TempDir includes the full test name; under macOS hosted TMPDIR that
	// exceeds the Unix socket path limit before the intended startup failure.
	// Keep the directory inside TestMain's isolated temp root, with a short name.
	dir, err := os.MkdirTemp("", "sf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	if runtime.GOOS == "windows" {
		socket = fmt.Sprintf(`\\.\pipe\oberth-start-failure-%d-%d`, os.Getpid(), time.Now().UnixNano())
	}
	var order []string
	var ready bool
	boom := errors.New("required recovery failed")
	OnStart(func(*Runtime) { order = append(order, "optional") })
	OnStartChecked(func(rt *Runtime) error {
		ready = rt.Store != nil && rt.DB != nil
		order = append(order, "required")
		return boom
	})
	OnStart(func(*Runtime) { order = append(order, "too-late") })
	OnShutdown(func(graceful bool) {
		order = append(order, "cleanup")
		if graceful {
			order = append(order, "wrong-graceful")
		}
	})
	var scans atomic.Int32
	loop := scanner.New(scanner.Options{
		Demand: func() (int, scanner.Include) { return 1, scanner.Include{} },
		Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
			scans.Add(1)
			return nil, nil
		},
	})
	srv := New(Options{Socket: socket, DBPath: filepath.Join(dir, "state.db"), Scanner: loop})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Serve(ctx); !errors.Is(err, boom) {
		t.Fatalf("Serve lost startup failure: %v", err)
	}
	if !ready || !reflect.DeepEqual(order, []string{"optional", "required", "cleanup"}) {
		t.Fatalf("startup/unwind order=%v runtime-ready=%v", order, ready)
	}
	if scans.Load() != 0 || srv.Clients() != 0 {
		t.Fatal("incomplete runtime started serving")
	}
	select {
	case <-srv.Done():
	default:
		t.Fatal("Serve did not finish")
	}
	// Bind and lock again: cleanup must not leave a wedged daemon endpoint.
	ln, err := listen(socket)
	if err != nil {
		t.Fatalf("failed startup retained listener: %v", err)
	}
	_ = ln.Close()
	_ = removeStaleSocket(socket)
	lock, err := AcquireLock(lockPathFor(socket))
	if err != nil {
		t.Fatalf("failed startup retained instance lock: %v", err)
	}
	_ = lock.Release()
	if srv.runtime.DB != nil && srv.runtime.DB.Ping() == nil {
		t.Fatal("failed startup retained open database")
	}
}
