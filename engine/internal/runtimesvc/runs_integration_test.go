//go:build integration

// Integration coverage for `oberth start` and the daemon's runs registry, which
// is spec integration test 2. Run them with
// `go test -tags integration ./internal/runtimesvc/...`.
package runtimesvc_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// buildListener compiles a helper that listens on the port it is handed and
// then blocks. Behind it are a real process and a real socket, so the daemon's
// own scan has to find it and attribute the port through the PPID chain.
var buildListener = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "option-berth-itest-listener")
	if err != nil {
		return "", err
	}
	source := filepath.Join(dir, "main.go")
	program := `package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	sock, err := net.Listen("tcp", "127.0.0.1:"+os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}
	defer sock.Close()
	fmt.Println("helper listening on", os.Args[1])
	for {
		time.Sleep(500 * time.Millisecond)
	}
}
`
	if err := os.WriteFile(source, []byte(program), 0o644); err != nil {
		return "", err
	}
	binary := filepath.Join(dir, "listener")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, source)
	if out, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building the listener helper: %v: %s", err, out)
	}
	return binary, nil
})

// freePort hands back a port nothing is listening on right now.
func freePort(t *testing.T) int {
	t.Helper()
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer reservation.Close()
	return reservation.Addr().(*net.TCPAddr).Port
}

// TestStartAttributesItsPortToTheGroup is spec integration test 2: a helper
// started through `oberth start --group itest --name web` turns up in a delta
// carrying the group, the source and the name the run gave it.
func TestStartAttributesItsPortToTheGroup(t *testing.T) {
	helper, err := buildListener()
	if err != nil {
		t.Fatal(err)
	}

	sandbox := newEnv(t)
	sandbox.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cl := sandbox.connect(ctx)

	sub, err := cl.Subscribe(ctx, client.SubscribeOptions{Events: true})
	if err != nil {
		t.Fatalf("state.subscribe: %v", err)
	}

	port := freePort(t)
	starter := sandbox.command("start", "--group", "itest", "--name", "web", "--port", strconv.Itoa(port),
		"--", helper, strconv.Itoa(port))
	starter.Dir = sandbox.home
	var log safeBuffer
	starter.Stdout, starter.Stderr = &log, &log
	ownProcessGroup(starter)
	if err := starter.Start(); err != nil {
		t.Fatalf("oberth start: %v", err)
	}
	t.Cleanup(func() {
		// The listener sits in a process group of its own and holds the pipe
		// `log` reads from, so the whole tree has to go before Wait can return.
		stopCommand(starter)
		if t.Failed() {
			t.Logf("oberth start output:\n%s", log.String())
		}
	})

	row, ok := waitForAttributedPort(t, sub, port, 45*time.Second)
	if !ok {
		t.Fatalf("no state.delta carried port %d with its run within 45s\n%s", port, log.String())
	}

	if row.Group == nil || *row.Group != "itest" {
		t.Errorf("group = %v, want itest", row.Group)
	}
	if row.GroupSource == nil || string(*row.GroupSource) != "start" {
		t.Errorf("group_source = %v, want start", row.GroupSource)
	}
	if row.DisplayName != "web" {
		t.Errorf("display_name = %q, want web", row.DisplayName)
	}
	if row.Run == nil {
		t.Fatalf("run is null; the port was not attributed to the run")
	}
	if row.Run.Name != "web" || row.Run.Group != "itest" {
		t.Errorf("run = %+v, want group itest name web", *row.Run)
	}
	if row.Run.RootPID <= 0 {
		t.Errorf("run.root_pid = %d, want the `oberth start` pid", row.Run.RootPID)
	}

	// The same run seen through runs.list, with the port it ended up binding.
	var runs rpc.RunsListResult
	if err := cl.Call(ctx, "runs.list", rpc.Empty{}, &runs); err != nil {
		t.Fatalf("runs.list: %v", err)
	}
	found := false
	for _, run := range runs.Runs {
		if run.Group != "itest" || run.Name != "web" {
			continue
		}
		found = true
		if run.Status != "running" {
			t.Errorf("status = %q, want running once the port is bound", run.Status)
		}
		if run.PortHint == nil || *run.PortHint != port {
			t.Errorf("port_hint = %v, want %d", run.PortHint, port)
		}
		if len(run.Ports) != 1 || run.Ports[0] != port {
			t.Errorf("ports = %v, want [%d]", run.Ports, port)
		}
	}
	if !found {
		t.Fatalf("runs.list did not carry the run: %+v", runs.Runs)
	}

	// Interrupting `oberth start` takes the listener down with it, and the run
	// leaves the registry.
	interrupt(t, starter)
	if !waitForRemoved(t, sub, port, 45*time.Second) {
		t.Fatalf("port %d never went away after interrupting oberth start", port)
	}
	waitForEmptyRunsList(t, ctx, cl, 20*time.Second)
}

// TestExitedRunSurvivesDaemonRestart is the durable half of the runs registry:
// a finished run observed by one daemon must still explain itself through
// runs.list after that daemon has been stopped and a new one has opened the
// same store.
func TestExitedRunSurvivesDaemonRestart(t *testing.T) {
	sandbox := newEnv(t)
	first := sandbox.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cl := sandbox.connect(ctx)

	var spawned rpc.RunsSpawnResult
	if err := cl.Call(ctx, "runs.spawn", rpc.RunsSpawnParams{
		Argv:  []string{"sh", "-c", "echo durable-exit; exit 7"},
		Cwd:   sandbox.home,
		Group: stringPtr("durability"),
		Name:  stringPtr("job"),
	}, &spawned); err != nil {
		t.Fatalf("runs.spawn: %v", err)
	}
	if spawned.RunID == "" {
		t.Fatal("runs.spawn returned no run id")
	}

	var before rpc.RunsListResult
	limit := time.Now().Add(20 * time.Second)
	for time.Now().Before(limit) {
		if err := cl.Call(ctx, "runs.list", rpc.Empty{}, &before); err != nil {
			t.Fatalf("runs.list before restart: %v", err)
		}
		if hasExitedRun(before.Exited, spawned.RunID) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !hasExitedRun(before.Exited, spawned.RunID) {
		t.Fatalf("run %s never appeared in exited list before restart: %+v", spawned.RunID, before)
	}

	sandbox.stopDaemon(first.Process.Pid)
	second := sandbox.serve()
	defer stopCommand(second)
	reconnected := sandbox.connect(ctx)
	var after rpc.RunsListResult
	if err := reconnected.Call(ctx, "runs.list", rpc.Empty{}, &after); err != nil {
		t.Fatalf("runs.list after restart: %v", err)
	}
	var reloaded *rpc.RunRecord
	for i := range after.Exited {
		if after.Exited[i].ID == spawned.RunID {
			reloaded = &after.Exited[i]
			break
		}
	}
	if reloaded == nil {
		t.Fatalf("run %s disappeared after daemon restart: %+v", spawned.RunID, after.Exited)
	}
	if reloaded.ExitCode == nil || *reloaded.ExitCode != 7 || reloaded.Reason != "crashed" {
		t.Fatalf("reloaded exit = %+v, want code 7/crashed", *reloaded)
	}
	if tail := reloaded.LastLines; len(tail) == 0 || tail[len(tail)-1] != "durable-exit" {
		t.Fatalf("reloaded log tail = %v, want durable-exit", tail)
	}
}

func stringPtr(s string) *string { return &s }

func hasExitedRun(exited []rpc.RunRecord, want string) bool {
	for _, record := range exited {
		if record.ID == want {
			return true
		}
	}
	return false
}

// interrupt asks a process to stop the way a terminal's Ctrl+C would.
func interrupt(t *testing.T, starter *exec.Cmd) {
	t.Helper()
	if starter.Process == nil {
		return
	}
	if runtime.GOOS == "windows" {
		// There is no SIGINT to send from another console on Windows; killing
		// the starter is what the Job Object turns into a tree kill.
		_ = starter.Process.Kill()
		return
	}
	if err := starter.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupting oberth start: %v", err)
	}
}

// waitForAttributedPort is waitForAdded, but it hands back the row so the test
// can assert on the group and the run the resolver put on it.
//
// It waits for the delta that carries the run, not the first delta that
// mentions the port: a scan tick can land between the child's bind and
// `oberth start` telling the daemon who owns it, and that tick publishes a real
// listener that genuinely has no run yet. The daemon corrects it in the very
// next delta, which is the row this test is about.
func waitForAttributedPort(t *testing.T, sub *client.Subscription, port int, timeout time.Duration) (state.Port, bool) {
	t.Helper()
	expired := time.After(timeout)
	for {
		select {
		case delta, open := <-sub.Deltas:
			if !open {
				return state.Port{}, false
			}
			for _, rows := range [][]state.Port{delta.Ports.Added, delta.Ports.Updated} {
				for _, row := range rows {
					if row.Port != port {
						continue
					}
					if row.Run != nil {
						return row, true
					}
					t.Logf("delta seq %d has port %d before its run was registered (group %v)",
						delta.Seq, port, row.Group)
				}
			}
		case <-expired:
			return state.Port{}, false
		}
	}
}

// waitForEmptyRunsList polls until the registry has pruned the finished run.
func waitForEmptyRunsList(t *testing.T, ctx context.Context, cl *client.Client, timeout time.Duration) {
	t.Helper()
	limit := time.Now().Add(timeout)
	for time.Now().Before(limit) {
		var runs rpc.RunsListResult
		if err := cl.Call(ctx, "runs.list", rpc.Empty{}, &runs); err != nil {
			t.Fatalf("runs.list: %v", err)
		}
		if len(runs.Runs) == 0 {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("the finished run was still in runs.list after %s", timeout)
}

// safeBuffer is a strings.Builder that survives the concurrent writes a
// command's stdout and stderr make.
type safeBuffer struct {
	mu   sync.Mutex
	text []byte
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.text = append(s.text, p...)
	return len(p), nil
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.text)
}
