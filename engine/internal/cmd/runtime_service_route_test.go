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

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
)

// routedRows is the scan the in-process runtime service serves. None of it is enriched,
// so the rows the runtime service publishes and the rows the direct path renders are
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

// startTestRuntimeService brings up a runtime service over a real socket whose scan answers with
// rows, and points the CLI's dialers at it for the rest of the test.
func startTestRuntimeService(t *testing.T, table []ports.ListeningPort) {
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

	server := runtimesvc.New(runtimesvc.Options{
		Scanner: scanner.New(scanner.Options{
			RuntimeServiceVersion: "test",
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
		t.Fatalf("runtime service did not come up: %v", err)
	}

	savedDialer := dialRuntimeService
	reach := func(ctx context.Context) (*client.Client, error) {
		return client.Dial(ctx, client.ClientInfo{Name: "cli", Version: "test", Socket: socket})
	}
	dialRuntimeService = reach
	t.Cleanup(func() { dialRuntimeService = savedDialer })

	// Reads and writes must both land on this runtime_service: a command that autostarts
	// would otherwise start a second runtime service of its own instead of reaching this
	// one, which is precisely what the harness exists to prevent.
	savedWrite := connectForWrite
	connectForWrite = reach
	t.Cleanup(func() { connectForWrite = savedWrite })
}

// noRuntimeServiceReachable makes every connection attempt fail, the way an empty
// socket path does — including the ones a write command makes, which otherwise
// would autostart a runtime service of their own.
func noRuntimeServiceReachable(t *testing.T) {
	t.Helper()
	unreachable := func(context.Context) (*client.Client, error) {
		return nil, errors.New("oberth runtime service is not running")
	}
	prev, prevWrite := dialRuntimeService, connectForWrite
	dialRuntimeService, connectForWrite = unreachable, unreachable
	t.Cleanup(func() {
		dialRuntimeService, connectForWrite = prev, prevWrite
	})
}

// resetRouting clears the per-invocation state the flags and the once-only note
// keep, so tests do not leak into each other.
func resetRouting(t *testing.T) {
	t.Helper()
	savedFlag := noRuntimeServiceFlag
	noRuntimeServiceFlag = false
	fallbackNoteOnce = sync.Once{}
	t.Cleanup(func() {
		noRuntimeServiceFlag = savedFlag
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
	noRuntimeServiceReachable(t)

	stderrText := captureStderr(t, func() {
		for range 3 {
			if conn := runtimeServiceClient(context.Background()); conn != nil {
				t.Error("runtimeServiceClient returned a client with no runtime service reachable")
				conn.Close()
			}
		}
	})
	if count := bytes.Count([]byte(stderrText), []byte("note: runtime service unavailable, using direct scan")); count != 1 {
		t.Fatalf("the fallback note appeared %d times, want exactly 1:\n%s", count, stderrText)
	}
}

// TestRuntimeServiceUpStaysSilentAboutTheFallback: a command that has nothing to fall
// back to asks whether a runtime service is there; "using direct scan" would be a false
// sentence for it, so the answer is a quiet false.
func TestRuntimeServiceUpStaysSilentAboutTheFallback(t *testing.T) {
	resetRouting(t)
	noRuntimeServiceReachable(t)

	out := captureStderr(t, func() {
		if runtimeServiceUp(context.Background()) {
			t.Error("runtimeServiceUp reported a runtime service that cannot be reached")
		}
	})
	if out != "" {
		t.Fatalf("runtimeServiceUp wrote %q, want no note", out)
	}
}

func TestNoRuntimeServiceFlagNeverDialsAndSaysNothing(t *testing.T) {
	resetRouting(t)
	noRuntimeServiceFlag = true

	saved := dialRuntimeService
	dialRuntimeService = func(context.Context) (*client.Client, error) {
		t.Error("--no-runtime-service still dialled the runtime service")
		return nil, errors.New("unreachable")
	}
	t.Cleanup(func() { dialRuntimeService = saved })

	stderrText := captureStderr(t, func() {
		if conn := runtimeServiceClient(context.Background()); conn != nil {
			t.Error("--no-runtime-service returned a client")
			conn.Close()
		}
	})
	if stderrText != "" {
		t.Fatalf("--no-runtime-service printed %q; it is a deliberate choice, not a fallback", stderrText)
	}
}
