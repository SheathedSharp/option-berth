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

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
)

// routedRows is the scan the in-process daemon serves. None of it is enriched,
// so the rows the daemon publishes and the rows the direct path renders are
// comparable byte for byte. Groups stay unset: both paths run the resolver, and
// it is the resolver's answer that has to agree.
func routedRows() []ports.ListeningPort {
	return []ports.ListeningPort{
		{
			BindAddress: "127.0.0.1", IPVersion: "IPv4", Type: ports.PortTypeUser,
			Port: 3000, PID: 100, Process: "node", Command: "node server.js",
			User: "dev", Cwd: "/home/dev/web",
		},
		{
			BindAddress: "0.0.0.0", IPVersion: "IPv4", Type: ports.PortTypeDocker,
			Port: 5432, PID: 200, Process: "com.docker.backend",
			DockerContainer: "db-1", DockerImage: "postgres:17",
			DockerComposeService: "db", DockerComposeProject: "shop",
			DockerContainerPort: 5432,
		},
		{
			BindAddress: "127.0.0.1", IPVersion: "IPv4", Type: ports.PortTypeUser,
			Port: 7000, PID: 300, Process: "Figma",
		},
	}
}

// startTestDaemon brings up a daemon over a real socket whose scan answers with
// rows, and points the CLI's dialers at it for the rest of the test.
func startTestDaemon(t *testing.T, table []ports.ListeningPort) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket harness does not apply to named pipes")
	}

	// Deliberately not t.TempDir(): a unix socket path is capped at ~104 bytes on
	// macOS, and the test name alone would blow that budget.
	socketDir, err := os.MkdirTemp("", "sn")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "d.sock")

	server := daemon.New(daemon.Options{
		Scanner: scanner.New(scanner.Options{
			DaemonVersion: "test",
			Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
				return append([]ports.ListeningPort{}, table...), nil
			},
		}),
		Socket:  socket,
		Version: "test",
	})
	ctx, stop := context.WithCancel(context.Background())
	go func() { _ = server.Serve(ctx) }()
	t.Cleanup(func() {
		stop()
		<-server.Done()
	})
	if err := client.WaitForSocket(ctx, socket, 5*time.Second); err != nil {
		t.Fatalf("daemon did not come up: %v", err)
	}

	savedDialer := dialDaemon
	reach := func(ctx context.Context) (*client.Client, error) {
		return client.Dial(ctx, client.ClientInfo{Name: "cli", Version: "test", Socket: socket})
	}
	dialDaemon = reach
	t.Cleanup(func() { dialDaemon = savedDialer })

	// Reads and writes must both land on this daemon: a command that autostarts
	// would otherwise start a second daemon of its own instead of reaching this
	// one, which is precisely what the harness exists to prevent.
	savedWrite := connectForWrite
	connectForWrite = reach
	t.Cleanup(func() { connectForWrite = savedWrite })
}

// noDaemonReachable makes every connection attempt fail, the way an empty
// socket path does — including the ones a write command makes, which otherwise
// would autostart a daemon of their own.
func noDaemonReachable(t *testing.T) {
	t.Helper()
	unreachable := func(context.Context) (*client.Client, error) {
		return nil, errors.New("oberth daemon is not running")
	}
	prev, prevWrite := dialDaemon, connectForWrite
	dialDaemon, connectForWrite = unreachable, unreachable
	t.Cleanup(func() {
		dialDaemon, connectForWrite = prev, prevWrite
	})
}

// resetRouting clears the per-invocation state the flags and the once-only note
// keep, so tests do not leak into each other.
func resetRouting(t *testing.T) {
	t.Helper()
	savedFlag := noDaemonFlag
	noDaemonFlag = false
	fallbackNoteOnce = sync.Once{}
	t.Cleanup(func() {
		noDaemonFlag = savedFlag
		fallbackNoteOnce = sync.Once{}
	})
}

// captureStderr collects what fn wrote to the real os.Stderr; the fallback note
// goes there.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stderr
	os.Stderr = writer

	collected := make(chan string, 1)
	go func() {
		var sink bytes.Buffer
		_, _ = io.Copy(&sink, reader)
		collected <- sink.String()
	}()

	fn()

	os.Stderr = saved
	writer.Close()
	text := <-collected
	reader.Close()
	return text
}

func TestFallbackNoteIsPrintedOncePerInvocation(t *testing.T) {
	resetRouting(t)
	noDaemonReachable(t)

	stderrText := captureStderr(t, func() {
		for range 3 {
			if conn := daemonClient(context.Background()); conn != nil {
				t.Error("daemonClient returned a client with no daemon reachable")
				conn.Close()
			}
		}
	})
	if count := bytes.Count([]byte(stderrText), []byte("note: daemon unavailable, using direct scan")); count != 1 {
		t.Fatalf("the fallback note appeared %d times, want exactly 1:\n%s", count, stderrText)
	}
}

// TestDaemonUpStaysSilentAboutTheFallback: a command that has nothing to fall
// back to asks whether a daemon is there; "using direct scan" would be a false
// sentence for it, so the answer is a quiet false.
func TestDaemonUpStaysSilentAboutTheFallback(t *testing.T) {
	resetRouting(t)
	noDaemonReachable(t)

	out := captureStderr(t, func() {
		if daemonUp(context.Background()) {
			t.Error("daemonUp reported a daemon that cannot be reached")
		}
	})
	if out != "" {
		t.Fatalf("daemonUp wrote %q, want no note", out)
	}
}

func TestNoDaemonFlagNeverDialsAndSaysNothing(t *testing.T) {
	resetRouting(t)
	noDaemonFlag = true

	saved := dialDaemon
	dialDaemon = func(context.Context) (*client.Client, error) {
		t.Error("--no-daemon still dialled the daemon")
		return nil, errors.New("unreachable")
	}
	t.Cleanup(func() { dialDaemon = saved })

	stderrText := captureStderr(t, func() {
		if conn := daemonClient(context.Background()); conn != nil {
			t.Error("--no-daemon returned a client")
			conn.Close()
		}
	})
	if stderrText != "" {
		t.Fatalf("--no-daemon printed %q; it is a deliberate choice, not a fallback", stderrText)
	}
}
