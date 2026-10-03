//go:build integration

// Spec integration test 4, in the current worktree-service shape: `oberth up`
// starts a three-service group in depends_on order and `oberth down` stops all
// three. Run with
// `go test -tags integration ./internal/daemon/...`.
package daemon_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// TestUpStartsAGroupInOrderAndDownStopsIt is the step's acceptance path end to
// end, through the real binary: a config with db, api and frontend, one of them
// waiting on another, started detached and then stopped as a worktree.
func TestUpStartsAGroupInOrderAndDownStopsIt(t *testing.T) {
	listener, buildErr := buildListener()
	if buildErr != nil {
		t.Fatal(buildErr)
	}

	e := newEnv(t)
	// The project lives inside the temp HOME: the daemon refuses to start a
	// command outside the user's home unless it is asked to.
	project := filepath.Join(e.home, "demo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	dbPort, apiPort, webPort := unusedPort(t), unusedPort(t), unusedPort(t)
	config := fmt.Sprintf(`# the demo project
name: demo
services:
  - name: db
    cmd: '%s %d'
    port: %d
  - name: api
    cmd: '%s %d'
    port: %d
    depends_on: [db]
  - name: frontend
    cmd: '%s %d'
    port: %d
`, listener, dbPort, dbPort, listener, apiPort, apiPort, listener, webPort, webPort)
	configPath := filepath.Join(project, groups.ConfigName)
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	e.serve()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// A subscriber keeps the daemon's scan loop running: with nobody
	// listening it parks, and the down events below are published from deltas.
	c := e.connect(ctx)
	sub, err := c.Subscribe(ctx, client.SubscribeOptions{Events: true, Buffer: 512})
	if err != nil {
		t.Fatalf("state.subscribe: %v", err)
	}

	// `oberth up` from inside the project: no argument, so the group comes from
	// the cwd's oberth.yaml.
	up := e.command("up", "--json")
	up.Dir = project
	upOut, err := up.CombinedOutput()
	if err != nil {
		t.Fatalf("oberth up: %v\n%s", err, upOut)
	}
	t.Logf("oberth up --json:\n%s", upOut)

	var summary struct {
		Services []rpc.GroupsStartChunk `json:"services"`
		Started  []string               `json:"started"`
		Skipped  []string               `json:"skipped"`
		Errors   []string               `json:"errors"`
	}
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
	if summary.Services[0].Service != "db" {
		t.Errorf("the first service started was %q, want db (nothing depends_on it)",
			summary.Services[0].Service)
	}
	var apiChunk rpc.GroupsStartChunk
	for _, c := range summary.Services {
		if c.Service == "api" {
			apiChunk = c
		}
		if c.PID <= 0 || c.LogPath == "" {
			t.Errorf("chunk %+v carries no pid or log path", c)
		}
		if !strings.Contains(c.LogPath, filepath.Join("logs", "demo", c.Service+".log")) {
			t.Errorf("log path %q is not ~/.option-berth/logs/demo/%s.log", c.LogPath, c.Service)
		}
	}
	if apiChunk.PID == 0 {
		t.Fatal("api never started")
	}

	// api only starts once db is listening; seeing all three ports in the
	// daemon snapshot confirms the dependency order reached its running state.
	waitForPorts(t, ctx, c, []int{dbPort, apiPort, webPort}, 45*time.Second)

	// Running `up` again is a no-op: everything is already running.
	again := e.command("up", "--json")
	again.Dir = project
	againOut, err := again.CombinedOutput()
	if err != nil {
		t.Fatalf("second oberth up: %v\n%s", err, againOut)
	}
	var second struct {
		Skipped []string `json:"skipped"`
		Started []string `json:"started"`
	}
	if err := json.Unmarshal(againOut, &second); err != nil {
		t.Fatalf("decoding the second oberth up: %v\n%s", err, againOut)
	}
	if len(second.Started) != 0 || len(second.Skipped) != 3 {
		t.Errorf("a second `oberth up` = started %v, skipped %v; want everything skipped",
			second.Started, second.Skipped)
	}

	// `oberth down` stops all three services declared by this worktree.
	down := e.command("down", "--json")
	down.Dir = project
	downOut, err := down.CombinedOutput()
	if err != nil {
		t.Fatalf("oberth down: %v\n%s", err, downOut)
	}
	t.Logf("oberth down --json:\n%s", downOut)

	for _, port := range []int{dbPort, apiPort, webPort} {
		if portOpen(port) {
			t.Errorf("port %d is still listening after `oberth down`", port)
		}
	}

	// The daemon publishes the three port_down events as the services leave.
	waitForDownEvents(t, sub, []int{dbPort, apiPort, webPort}, 45*time.Second)
}

// waitForDownEvents drains the subscription until every port has been reported
// as gone.
func waitForDownEvents(t *testing.T, sub *client.Subscription, want []int, timeout time.Duration) {
	t.Helper()
	pending := map[int]bool{}
	for _, p := range want {
		pending[p] = true
	}
	deadline := time.After(timeout)
	for len(pending) > 0 {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				t.Fatal("the subscription closed before the ports went down")
			}
			if ev.Kind == "port_down" && ev.Port != nil {
				delete(pending, ev.Port.Port)
			}
		case <-sub.Deltas:
			// Deltas have to be drained or the subscription is dropped.
		case <-deadline:
			t.Fatalf("no port_down event for %v within %s", keys(pending), timeout)
		}
	}
}

func keys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func waitForPorts(t *testing.T, ctx context.Context, c *client.Client, want []int, timeout time.Duration) {
	t.Helper()
	pending := map[int]bool{}
	for _, port := range want {
		pending[port] = true
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var snap state.Snapshot
		if err := c.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snap); err != nil {
			t.Fatalf("state.snapshot: %v", err)
		}
		for _, p := range snap.Ports {
			delete(pending, p.Port)
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
	seen := map[int]bool{}
	for i := 0; i < 50; i++ {
		port := freePort(t)
		if seen[port] || portOpen(port) {
			continue
		}
		seen[port] = true
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
	conn.Close()
	return true
}

// killChunks stops anything a failed run left behind.
func killChunks(chunks []rpc.GroupsStartChunk) {
	for _, c := range chunks {
		if c.PID <= 0 {
			continue
		}
		if p, err := os.FindProcess(c.PID); err == nil {
			_ = p.Kill()
		}
	}
}
