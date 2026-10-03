package daemon

import (
	"context"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/ports"
)

// fakeRows is the snapshot every read handler is exercised against: a user
// process, a Docker row, a desktop app and a second bind of the same port.
//
// Groups are not set here: the scan tick resolves them, so a row's group is
// whatever the resolver derives — for the Docker rows, the Compose project.
func fakeRows() []ports.ListeningPort {
	return []ports.ListeningPort{
		{
			Port: 3000, PID: 100, Process: "node", Command: "node server.js",
			BindAddress: "127.0.0.1", IPVersion: "IPv4", Type: ports.PortTypeUser,
			Cwd: "/home/dev/web",
		},
		{
			Port: 3000, PID: 101, Process: "node", Command: "node server.js",
			BindAddress: "::1", IPVersion: "IPv6", Type: ports.PortTypeUser,
		},
		{
			Port: 5432, PID: 200, Process: "com.docker.backend",
			BindAddress: "0.0.0.0", IPVersion: "IPv4", Type: ports.PortTypeDocker,
			DockerContainer: "db-1", DockerImage: "postgres:17", DockerComposeService: "db",
			DockerComposeProject: "shop",
		},
		{
			Port: 7000, PID: 300, Process: "Figma",
			BindAddress: "127.0.0.1", IPVersion: "IPv4", Type: ports.PortTypeUser,
		},
		{
			Port: 9000, PID: 400, Process: "python3", BindAddress: "127.0.0.1",
			IPVersion: "IPv4", Type: ports.PortTypeUser,
			Tag: "api", RunID: "run-7", RunRootPID: 399,
		},
	}
}

// newPortsHarness starts a server whose scanner returns fakeRows
// and returns a connected test client.
func newPortsHarness(t *testing.T, ctx context.Context) *testClient {
	t.Helper()
	h := newHarness(t, ctx)
	h.setRows(fakeRows()...)
	return h.dial(ctx)
}
