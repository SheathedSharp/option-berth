package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon"
	"github.com/sheathedsharp/option-berth/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/scanner"
)

// routedRows is the scan the in-process daemon serves. Nothing here is
// enriched, so what the daemon publishes and what the direct path renders from
// the same rows are comparable byte for byte. Groups are left unset: both paths
// run the resolver, and it is the resolver's answer that has to agree.
func routedRows() []ports.ListeningPort {
	return []ports.ListeningPort{
		{
			Port: 3000, PID: 100, Process: "node", Command: "node server.js",
			BindAddress: "127.0.0.1", IPVersion: "IPv4", Type: ports.PortTypeUser,
			User: "dev", Cwd: "/home/dev/web",
		},
		{
			Port: 5432, PID: 200, Process: "com.docker.backend",
			BindAddress: "0.0.0.0", IPVersion: "IPv4", Type: ports.PortTypeDocker,
			DockerContainer: "db-1", DockerImage: "postgres:17",
			DockerComposeService: "db", DockerComposeProject: "shop",
			DockerContainerPort: 5432,
		},
		{
			Port: 7000, PID: 300, Process: "Figma",
			BindAddress: "127.0.0.1", IPVersion: "IPv4", Type: ports.PortTypeUser,
		},
	}
}

// startTestDaemon runs a daemon over a real socket with a fixed scan result and
// points the CLI's dialer at it for the rest of the test.
func startTestDaemon(t *testing.T, rows []ports.ListeningPort) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket harness does not apply to named pipes")
	}

	// Not t.TempDir(): a unix socket path is capped at ~104 bytes on macOS and
	// the test name would blow the budget.
	dir, err := os.MkdirTemp("", "sn")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")

	srv := daemon.New(daemon.Options{
		Socket:  socket,
		Version: "test",
		Scanner: scanner.New(scanner.Options{
			DaemonVersion: "test",
			Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
				return append([]ports.ListeningPort{}, rows...), nil
			},
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-srv.Done()
	})
	if err := client.WaitForSocket(ctx, socket, 5*time.Second); err != nil {
		t.Fatalf("daemon did not come up: %v", err)
	}

	prev := dialDaemon
	dialDaemon = func(ctx context.Context) (*client.Client, error) {
		return client.Dial(ctx, client.ClientInfo{Name: "cli", Version: "test", Socket: socket})
	}
	t.Cleanup(func() { dialDaemon = prev })
}

// noDaemonReachable makes every connection attempt fail, the way an empty
// socket path does.
func noDaemonReachable(t *testing.T) {
	t.Helper()
	prev := dialDaemon
	dialDaemon = func(context.Context) (*client.Client, error) {
		return nil, errors.New("oberth daemon is not running")
	}
	t.Cleanup(func() { dialDaemon = prev })
}

// resetRouting clears the per-invocation state the flags and the once-only note
// keep, so tests do not leak into each other.
func resetRouting(t *testing.T) {
	t.Helper()
	prevFlag := noDaemonFlag
	noDaemonFlag = false
	fallbackNoteOnce = sync.Once{}
	t.Cleanup(func() {
		noDaemonFlag = prevFlag
		fallbackNoteOnce = sync.Once{}
	})
}

// captureStderr collects what fn writes to the real os.Stderr, which is where
// the fallback note goes.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stderr
	os.Stderr = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	os.Stderr = prev
	w.Close()
	out := <-done
	r.Close()
	return out
}

func TestFallbackNoteIsPrintedOncePerInvocation(t *testing.T) {
	resetRouting(t)
	noDaemonReachable(t)

	out := captureStderr(t, func() {
		for range 3 {
			if c := daemonClient(context.Background()); c != nil {
				t.Error("daemonClient returned a client with no daemon reachable")
				c.Close()
			}
		}
	})
	if n := bytes.Count([]byte(out), []byte("note: daemon unavailable, using direct scan")); n != 1 {
		t.Fatalf("the fallback note appeared %d times, want exactly 1:\n%s", n, out)
	}
}

func TestNoDaemonFlagNeverDialsAndSaysNothing(t *testing.T) {
	resetRouting(t)
	noDaemonFlag = true

	prev := dialDaemon
	dialDaemon = func(context.Context) (*client.Client, error) {
		t.Error("--no-daemon still dialled the daemon")
		return nil, errors.New("unreachable")
	}
	t.Cleanup(func() { dialDaemon = prev })

	out := captureStderr(t, func() {
		if c := daemonClient(context.Background()); c != nil {
			t.Error("--no-daemon returned a client")
			c.Close()
		}
	})
	if out != "" {
		t.Fatalf("--no-daemon printed %q; it is a deliberate choice, not a fallback", out)
	}
}
