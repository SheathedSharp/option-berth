//go:build integration

// Spec integration test 4, in the worktree-service shape it has today: `oberth
// up` starts a three-service group in depends_on order and `oberth down` stops
// all three. Run with
// `go test -tags integration ./internal/daemon/...`.
package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// upSummary is the `oberth up --json` document: what started, what was already
// running, and what failed.
type upSummary struct {
	Services []rpc.GroupsStartChunk `json:"services"`
	Started  []string               `json:"started"`
	Skipped  []string               `json:"skipped"`
	Errors   []string               `json:"errors"`
}

// TestUpStartsAGroupInOrderAndDownStopsIt is the acceptance path end to end
// through the real binary: a config with db, api and frontend, one of them
// waiting on another, started detached and then stopped as a worktree.
func TestUpStartsAGroupInOrderAndDownStopsIt(t *testing.T) {
	helper, buildErr := buildListener()
	if buildErr != nil {
		t.Fatal(buildErr)
	}

	sandbox := newEnv(t)
	// The project lives inside the temp HOME: the daemon refuses to start a
	// command outside the user's home unless it is asked to.
	project := filepath.Join(sandbox.home, "demo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("making the project directory: %v", err)
	}

	dbPort := unusedPort(t)
	apiPort := unusedPort(t)
	webPort := unusedPort(t)
	config := fmt.Sprintf(`# a demo project whose api depends on its db
name: demo
services:
  # the database, and the service api waits on
  - name: db
    cmd: '%s %d'
    port: %d
  # the api, which may only start once db is listening
  - name: api
    cmd: '%s %d'
    port: %d
    depends_on: [db]
  # a service nothing depends on
  - name: frontend
    cmd: '%s %d'
    port: %d
`, helper, dbPort, dbPort, helper, apiPort, apiPort, helper, webPort, webPort)
	configPath := filepath.Join(project, groups.ConfigName)
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	sandbox.serve()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// A subscriber keeps the daemon's scan loop running: with nobody listening
	// it parks, and the down events below are published from deltas.
	cl := sandbox.connect(ctx)
	sub, err := cl.Subscribe(ctx, client.SubscribeOptions{Events: true, Buffer: 512})
	if err != nil {
		t.Fatalf("state.subscribe: %v", err)
	}

	// `oberth up` from inside the project: no argument, so the group comes from
	// the cwd's oberth.yaml.
	up := sandbox.command("up", "--json")
	up.Dir = project
	upOut, upErr, err := runSplit(up)
	if err != nil {
		t.Fatalf("oberth up: %v\nstdout:\n%s\nstderr:\n%s", err, upOut, upErr)
	}
	t.Logf("oberth up --json:\n%s\nstderr:\n%s", upOut, upErr)

	var summary upSummary
	if err := json.Unmarshal(upOut, &summary); err != nil {
		t.Fatalf("decoding oberth up --json: %v\n%s", err, upOut)
	}
	// Registered before the first assertion: a failure half way through must
	// not leave three detached listeners squatting on ports for the next run.
	t.Cleanup(func() { killChunks(summary.Services) })

	if len(summary.Errors) != 0 {
		t.Fatalf("oberth up reported failures: %+v", summary)
	}
	if len(summary.Started) != 3 {
		t.Fatalf("started = %v, want all three services", summary.Started)
	}
	if first := summary.Services[0].Service; first != "db" {
		t.Errorf("the first service started was %q, want db (nothing depends_on it)", first)
	}
	var apiChunk rpc.GroupsStartChunk
	for _, chunk := range summary.Services {
		if chunk.Service == "api" {
			apiChunk = chunk
		}
		if chunk.PID <= 0 || chunk.LogPath == "" {
			t.Errorf("chunk %+v carries no pid or log path", chunk)
		}
		if !strings.Contains(chunk.LogPath, filepath.Join("logs", "demo", chunk.Service+".log")) {
			t.Errorf("log path %q is not ~/.option-berth/logs/demo/%s.log", chunk.LogPath, chunk.Service)
		}
	}
	if apiChunk.PID == 0 {
		t.Fatal("api never started")
	}

	// api only starts once db is listening; seeing all three ports in the
	// daemon snapshot confirms the dependency order reached its running state.
	waitForPorts(t, ctx, cl, []int{dbPort, apiPort, webPort}, 45*time.Second)

	// Running `up` again is a no-op: everything is already running.
	again := sandbox.command("up", "--json")
	again.Dir = project
	againOut, againErr, err := runSplit(again)
	if err != nil {
		t.Fatalf("second oberth up: %v\nstdout:\n%s\nstderr:\n%s", err, againOut, againErr)
	}
	var second upSummary
	if err := json.Unmarshal(againOut, &second); err != nil {
		t.Fatalf("decoding the second oberth up: %v\n%s", err, againOut)
	}
	if len(second.Started) != 0 || len(second.Skipped) != 3 {
		t.Errorf("a second `oberth up` = started %v, skipped %v; want everything skipped",
			second.Started, second.Skipped)
	}

	// `oberth down` stops all three services declared by this worktree.
	down := sandbox.command("down", "--json")
	down.Dir = project
	downOut, downErr, err := runSplit(down)
	if err != nil {
		t.Fatalf("oberth down: %v\nstdout:\n%s\nstderr:\n%s", err, downOut, downErr)
	}
	t.Logf("oberth down --json:\n%s\nstderr:\n%s", downOut, downErr)

	for _, port := range []int{dbPort, apiPort, webPort} {
		if portOpen(port) {
			t.Errorf("port %d is still listening after `oberth down`", port)
		}
	}

	// The daemon publishes the three port_down events as the services leave.
	waitForDownEvents(t, sub, []int{dbPort, apiPort, webPort}, 45*time.Second)
}

// waitForDownEvents drains the subscription until the daemon has reported every
// port as gone.
func waitForDownEvents(t *testing.T, sub *client.Subscription, want []int, timeout time.Duration) {
	t.Helper()
	pending := make(map[int]bool, len(want))
	for _, port := range want {
		pending[port] = true
	}
	expired := time.After(timeout)
	for len(pending) > 0 {
		select {
		case event, open := <-sub.Events:
			if !open {
				t.Fatal("the subscription closed before the ports went down")
			}
			if event.Kind == "port_down" && event.Port != nil {
				delete(pending, event.Port.Port)
			}
		case <-sub.Deltas:
			// Deltas have to be drained or the subscription is dropped.
		case <-expired:
			t.Fatalf("no port_down event for %v within %s", keys(pending), timeout)
		}
	}
}

func keys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for port := range m {
		out = append(out, port)
	}
	return out
}

// runSplit runs cmd with stdout and stderr captured apart. `up --json` writes
// in-flight progress to stderr, so only stdout is a JSON document; merging the
// two streams would corrupt it.
func runSplit(cmd *exec.Cmd) (stdout, stderr []byte, err error) {
	var out, problems bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &problems
	err = cmd.Run()
	return out.Bytes(), problems.Bytes(), err
}

func waitForPorts(t *testing.T, ctx context.Context, cl *client.Client, want []int, timeout time.Duration) {
	t.Helper()
	pending := map[int]bool{}
	for _, port := range want {
		pending[port] = true
	}
	limit := time.Now().Add(timeout)
	for time.Now().Before(limit) {
		var snapshot state.Snapshot
		if err := cl.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snapshot); err != nil {
			t.Fatalf("state.snapshot: %v", err)
		}
		for _, row := range snapshot.Ports {
			delete(pending, row.Port)
		}
		if len(pending) == 0 {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("ports %v never appeared in state.snapshot", keys(pending))
}

// unusedPort is freePort plus the check freePort cannot make: that nothing is
// actually answering there. A run that was killed mid-flight leaves detached
// services behind, and a config pointing at one of their ports would make
// `oberth up` skip everything as "already running".
func unusedPort(t *testing.T) int {
	t.Helper()
	tried := map[int]bool{}
	for attempt := 0; attempt < 50; attempt++ {
		port := freePort(t)
		if tried[port] || portOpen(port) {
			continue
		}
		tried[port] = true
		return port
	}
	t.Fatal("could not find a port nothing is listening on")
	return 0
}

func portOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

// killChunks stops anything a failed run left behind.
func killChunks(chunks []rpc.GroupsStartChunk) {
	for _, chunk := range chunks {
		if chunk.PID <= 0 {
			continue
		}
		process, err := os.FindProcess(chunk.PID)
		if err != nil {
			continue
		}
		_ = process.Kill()
	}
}
