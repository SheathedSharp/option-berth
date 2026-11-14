//go:build integration

package runtimesvc_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// replyBudget is what "does not wait on Docker" means below: far under the
// fake docker's hang, and far under the 10 s a client gives up after.
const replyBudget = 2 * time.Second

// fakeDockerOnPath puts a `docker` shell script first on PATH. Each run of it
// appends its arguments to a log file and then executes body. The first result
// is the environment entry that pins PATH, the second the log dockerCalls reads.
func fakeDockerOnPath(t *testing.T, body string) (pathEntry, logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake docker is a shell script")
	}
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + logPath + "'\n" + body
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return "PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"), logPath
}

// dockerCalls counts the logged runs whose first argument was verb.
func dockerCalls(logPath, verb string) int {
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return 0
	}
	seen := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == verb {
			seen++
		}
	}
	return seen
}

// portRow is the snapshot row for one port, or nil when the snapshot has none.
func portRow(snapshot state.Snapshot, port int) *state.Port {
	for i := range snapshot.Ports {
		if snapshot.Ports[i].Port == port {
			return &snapshot.Ports[i]
		}
	}
	return nil
}

// snapshotReader binds state.snapshot to one client, because the tests below
// poll it while they wait for the watcher to catch up.
func snapshotReader(t *testing.T, ctx context.Context, cl *client.Client) func() state.Snapshot {
	t.Helper()
	return func() state.Snapshot {
		t.Helper()
		var snapshot state.Snapshot
		if err := cl.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snapshot); err != nil {
			t.Fatalf("state.snapshot: %v", err)
		}
		return snapshot
	}
}

// TestAHungDockerDoesNotStallKillOrSnapshot is the regression test. The fake
// Docker never answers, so `docker ps` hangs; the daemon's scans used to wait
// out the CLI timeout on every call, which stretched a `ports.kill` past 25 s
// and timed out the `state.snapshot` calls queued behind it.
func TestAHungDockerDoesNotStallKillOrSnapshot(t *testing.T) {
	pathEntry, logPath := fakeDockerOnPath(t, "exec sleep 60\n")
	helper, err := buildListener()
	if err != nil {
		t.Fatal(err)
	}

	sandbox := newEnv(t)
	sandbox.extra = []string{pathEntry}
	sandbox.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cl := sandbox.connect(ctx)

	port := freePort(t)
	startListener(t, helper, sandbox.home, port)

	// The kill has to land while a `docker ps` is hanging, so first give the
	// daemon a moment to ask the fake. A daemon that asks Docker only from
	// inside a scan (the bug) has not asked yet, and its kill pays for the
	// hang itself.
	asked := time.Now().Add(5 * time.Second)
	for dockerCalls(logPath, "ps") == 0 && time.Now().Before(asked) {
		time.Sleep(50 * time.Millisecond)
	}

	killBegan := time.Now()
	var envelope rpc.KillEnvelope
	if err := cl.Call(ctx, "ports.kill", rpc.PortsKillParams{
		Targets: []rpc.Selector{{Port: &port}},
	}, &envelope); err != nil {
		t.Fatalf("ports.kill: %v", err)
	}
	killTook := time.Since(killBegan)
	if len(envelope.Results) == 0 || !envelope.Results[0].OK {
		t.Fatalf("ports.kill did not kill the listener: %+v", envelope.Results)
	}
	if method := envelope.Results[0].Method; method == state.MethodDockerStop {
		t.Errorf("the listener was stopped with %s", method)
	}
	t.Logf("ports.kill took %s", killTook)
	if killTook > replyBudget {
		t.Errorf("ports.kill took %s with docker hanging, want under %s", killTook, replyBudget)
	}

	// Three snapshots, spaced past the RPC cache so that each one really scans.
	read := snapshotReader(t, ctx, cl)
	for round := 1; round <= 3; round++ {
		if round > 1 {
			time.Sleep(2100 * time.Millisecond)
		}
		began := time.Now()
		snapshot := read()
		took := time.Since(began)
		t.Logf("state.snapshot %d took %s", round, took)
		if took > replyBudget {
			t.Errorf("state.snapshot %d took %s with docker hanging, want under %s", round, took, replyBudget)
		}
		for _, p := range snapshot.Ports {
			if p.Port == port {
				t.Errorf("snapshot %d still lists the killed port %d", round, port)
			}
		}
	}

	// Backoff: one hung attempt, then 5 s before the next. Scans poked the
	// watcher dozens of times meanwhile; none of them may have reached Docker.
	attempts := dockerCalls(logPath, "ps")
	t.Logf("docker ps ran %d times in the %s since the kill began", attempts, time.Since(killBegan))
	if attempts == 0 {
		t.Error("the daemon never ran docker ps, so this test proved nothing")
	}
	if attempts > 3 {
		t.Errorf("docker ps ran %d times against a hung Docker; the watcher is not backing off", attempts)
	}
}

// TestAHealthyDockerStillEnrichesAndGroups is the other half: a Docker that
// answers still gives the port it publishes its container, its compose labels
// and the compose project's group — now out of the watcher's cache.
func TestAHealthyDockerStillEnrichesAndGroups(t *testing.T) {
	helper, err := buildListener()
	if err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	composeDir := t.TempDir()
	psLine := "itest-web-1\tnginx:itest\t0.0.0.0:" + strconv.Itoa(port) + "->80/tcp\tweb\titestproj\t" + composeDir
	pathEntry, logPath := fakeDockerOnPath(t,
		"case \"$1\" in\nps) cat <<'EOF'\n"+psLine+"\nEOF\n;;\n*) exit 1 ;;\nesac\n")

	sandbox := newEnv(t)
	sandbox.extra = []string{pathEntry}
	sandbox.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cl := sandbox.connect(ctx)

	// Since the forwarder rule landed, only a port Docker's own forwarder holds
	// takes container data, so the helper runs under that name.
	startListener(t, forwarderCopy(t, helper), sandbox.home, port)

	read := snapshotReader(t, ctx, cl)
	var found *state.Port
	limit := time.Now().Add(20 * time.Second)
	for found == nil || found.Docker == nil {
		if time.Now().After(limit) {
			t.Fatalf("port %d was never enriched from docker (docker ps ran %d times); last row: %+v",
				port, dockerCalls(logPath, "ps"), found)
		}
		time.Sleep(250 * time.Millisecond)
		found = portRow(read(), port)
	}

	container := found.Docker
	if found.Type != state.TypeDocker {
		t.Errorf("type = %s, want docker", found.Type)
	}
	if container.Container != "itest-web-1" || container.Image != "nginx:itest" ||
		container.ComposeService != "web" || container.ComposeProject != "itestproj" ||
		container.ContainerPort != 80 {
		t.Errorf("docker = %+v", *container)
	}
	if found.Group == nil || *found.Group != "itestproj" {
		t.Errorf("group = %v, want the compose project itestproj", deref(found.Group))
	}
}

// forwarderCopy copies the listener helper under the name of this platform's
// Docker port forwarder, so the scanner reports the helper the way it reports a
// real published container port: "com.docke" through lsof on macOS,
// "docker-proxy" through ss on Linux.
func forwarderCopy(t *testing.T, helper string) string {
	t.Helper()
	name := "com.docker.backend"
	if runtime.GOOS == "linux" {
		name = "docker-proxy"
	}
	image, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(copied, image, 0o755); err != nil {
		t.Fatal(err)
	}
	return copied
}

// TestAStaleContainerListDoesNotClaimANativePort is the regression test for
// issue #95. Docker answers once, publishing the port as itest-web-1, then
// wedges so the watcher keeps serving that list; a native process then takes
// the port. Its row must not carry the container, and `ports.kill` must signal
// the process instead of running `docker stop`.
func TestAStaleContainerListDoesNotClaimANativePort(t *testing.T) {
	helper, err := buildListener()
	if err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	wedge := filepath.Join(t.TempDir(), "wedged")
	psLine := "itest-web-1\tnginx:itest\t0.0.0.0:" + strconv.Itoa(port) + "->80/tcp\tweb\titestproj\t" + t.TempDir()
	pathEntry, logPath := fakeDockerOnPath(t, "case \"$1\" in\nps)\n"+
		"  if [ -e '"+wedge+"' ]; then exec sleep 60; fi\n"+
		"  cat <<'EOF'\n"+psLine+"\nEOF\n;;\n*) exit 1 ;;\nesac\n")

	sandbox := newEnv(t)
	sandbox.extra = []string{pathEntry}
	sandbox.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cl := sandbox.connect(ctx)
	read := snapshotReader(t, ctx, cl)

	// Let the watcher cache the list: the second `docker ps` only starts once
	// the first has answered, and the snapshots keep the scanner — and with it
	// the watcher — running.
	limit := time.Now().Add(20 * time.Second)
	for dockerCalls(logPath, "ps") < 2 {
		if time.Now().After(limit) {
			t.Fatalf("docker ps ran %d times, want 2 before wedging it", dockerCalls(logPath, "ps"))
		}
		read()
		time.Sleep(250 * time.Millisecond)
	}

	// Docker wedges; from here on the cached list can only go stale.
	if err := os.WriteFile(wedge, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	startListener(t, helper, sandbox.home, port)

	var found *state.Port
	limit = time.Now().Add(20 * time.Second)
	for found == nil {
		if time.Now().After(limit) {
			t.Fatalf("port %d never showed up", port)
		}
		time.Sleep(250 * time.Millisecond)
		found = portRow(read(), port)
	}
	t.Logf("row: process=%q type=%s docker=%+v", found.Process, found.Type, found.Docker)
	if found.Docker != nil || found.Type == state.TypeDocker {
		t.Errorf("the native listener's row carries the cached container: type=%s docker=%+v",
			found.Type, found.Docker)
	}

	killBegan := time.Now()
	var envelope rpc.KillEnvelope
	if err := cl.Call(ctx, "ports.kill", rpc.PortsKillParams{
		Targets: []rpc.Selector{{Port: &port}},
	}, &envelope); err != nil {
		t.Fatalf("ports.kill: %v", err)
	}
	t.Logf("ports.kill took %s: %+v", time.Since(killBegan), envelope.Results)
	if len(envelope.Results) == 0 {
		t.Fatal("ports.kill returned no rows")
	}
	if row := envelope.Results[0]; !row.OK || row.Method == state.MethodDockerStop {
		t.Errorf("ports.kill row = %+v, want the listener signalled", row)
	}
	if stopped := dockerCalls(logPath, "stop"); stopped != 0 {
		t.Errorf("docker stop ran %d times for a port a native process held", stopped)
	}
	limit = time.Now().Add(5 * time.Second)
	for portOpen(port) {
		if time.Now().After(limit) {
			t.Fatalf("port %d is still open after the kill", port)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
