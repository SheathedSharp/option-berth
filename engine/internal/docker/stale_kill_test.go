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
// stops. Execute and escalate are not reached (see killer.Options.DryRun).
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

func listeningOn3000(process string) ports.ListeningPort {
	return ports.ListeningPort{
		Port: 3000, PID: os.Getpid(), Process: process, BindAddress: "127.0.0.1",
	}
}

// TestAStaleContainerPortHeldByANativeProcessKillsTheProcess is issue #95:
// the cached list still says a container publishes 3000, but a native
// process took the port over. The planned stop must be a signal to that
// process, never a docker stop of the stale container.
func TestAStaleContainerPortHeldByANativeProcessKillsTheProcess(t *testing.T) {
	pid := os.Getpid()
	row := listeningOn3000("node")
	row.Type = ports.PortTypeUser
	rows := []ports.ListeningPort{row}
	docker.EnrichFromPS(rows, []byte(cachedPS))

	if rows[0].Type == ports.PortTypeDocker || rows[0].DockerContainer != "" {
		t.Fatalf("the cached container was stamped onto a port node holds: %+v", rows[0])
	}
	if rows[0].PID != pid {
		t.Fatalf("enrichment changed the pid from %d to %d", pid, rows[0].PID)
	}

	got := planKill(t, rows)
	if got.Method == state.MethodDockerStop {
		t.Fatalf("the kill would docker-stop a container for a port node holds: %+v", got)
	}
	if got.Method != state.MethodSIGTERM || got.PID != pid || !got.OK {
		t.Errorf("plan = %+v, want SIGTERM to pid %d and OK", got, pid)
	}
}

// TestAContainerPortHeldByTheForwarderStopsTheContainer is the other half:
// the same cached list, when the holder is Docker's forwarder, still plans
// a container stop and must not fall back to signalling the forwarder pid.
func TestAContainerPortHeldByTheForwarderStopsTheContainer(t *testing.T) {
	rows := []ports.ListeningPort{listeningOn3000("com.docke")}
	docker.EnrichFromPS(rows, []byte(cachedPS))

	if rows[0].Type != ports.PortTypeDocker || rows[0].DockerContainer != "itest-web-1" {
		t.Fatalf("a port the forwarder holds lost its container: %+v", rows[0])
	}

	got := planKill(t, rows)
	if got.Method != state.MethodDockerStop || got.PID != 0 || !got.OK {
		t.Errorf("plan = %+v, want docker_stop with no process pid", got)
	}
	if got.Method == state.MethodSIGTERM {
		t.Errorf("a forwarder-held port was planned as a process signal: %+v", got)
	}
}
