package docker_test

import (
	"context"
	"os"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/docker"
	"github.com/sheathedsharp/option-berth/engine/internal/killer"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// cachedPS is a container list the watcher could still be serving after
// Docker stopped answering: itest-web-1 published 3000.
const cachedPS = "itest-web-1\tnginx:itest\t0.0.0.0:3000->80/tcp\tweb\titestproj\t/src/app\n"

// planKill answers what `ports.kill` on port 3000 would do with rows, without
// doing any of it: the dry run resolves the owner and picks a method, then
// stops.
func planKill(t *testing.T, rows []ports.ListeningPort) killer.Result {
	t.Helper()
	const port = 3000
	planned := killer.KillPorts(context.Background(),
		[]killer.Target{{Port: port}},
		killer.Options{Ports: rows, DryRun: true})
	if len(planned) != 1 {
		t.Fatalf("plan for port %d = %+v, want exactly one row", port, planned)
	}
	return planned[0]
}

// TestAStaleContainerPortHeldByANativeProcessKillsTheProcess is issue #95: the
// cached list still says a container publishes 3000, but a native process
// took the port over. The kill must signal that process, not `docker stop`
// the container.
func TestAStaleContainerPortHeldByANativeProcessKillsTheProcess(t *testing.T) {
	// A real pid, so the dry run can resolve it; nothing is signalled either
	// way, which is the point of DryRun.
	pid := os.Getpid()
	rows := []ports.ListeningPort{{
		Port: 3000, PID: pid, Process: "node",
		BindAddress: "127.0.0.1", Type: ports.PortTypeUser,
	}}
	docker.EnrichFromPS(rows, []byte(cachedPS))

	if rows[0].Type == ports.PortTypeDocker || rows[0].DockerContainer != "" {
		t.Fatalf("the cached container was stamped onto a port node holds: %+v", rows[0])
	}
	r := planKill(t, rows)
	if r.Method == state.MethodDockerStop {
		t.Fatalf("the kill would docker-stop a container for a port node holds: %+v", r)
	}
	if r.Method != state.MethodSIGTERM || r.PID != pid || !r.OK {
		t.Errorf("row = %+v, want SIGTERM to pid %d", r, pid)
	}
}

// TestAContainerPortHeldByTheForwarderStopsTheContainer is the other half: the
// same port held by Docker's forwarder still stops the container.
func TestAContainerPortHeldByTheForwarderStopsTheContainer(t *testing.T) {
	rows := []ports.ListeningPort{{
		Port: 3000, PID: os.Getpid(), Process: "com.docke", BindAddress: "127.0.0.1",
	}}
	docker.EnrichFromPS(rows, []byte(cachedPS))

	if rows[0].Type != ports.PortTypeDocker || rows[0].DockerContainer != "itest-web-1" {
		t.Fatalf("a port the forwarder holds lost its container: %+v", rows[0])
	}
	r := planKill(t, rows)
	if r.Method != state.MethodDockerStop || r.PID != 0 || !r.OK {
		t.Errorf("row = %+v, want docker_stop", r)
	}
}
