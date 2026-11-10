package docker

import (
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// forwarderSpellings are what the three scanners really report for Docker's
// port forwarders, including the truncations lsof (9 characters) and comm
// (15) apply, the Windows suffix, and the paths a process name may arrive as.
var forwarderSpellings = []string{
	"com.docke",              // macOS lsof, truncated to 9 (verified on a real machine)
	"com.docker.backend",     // lsof +c 0, or ps comm basename
	"com.docker.back",        // Linux comm, truncated to 15
	"com.docker.backend.exe", // Windows tasklist
	"COM.DOCKER.BACKEND.EXE", // Windows filenames are case-insensitive
	"vpnkit",                 // older Docker Desktop
	"vpnkit.exe",             // older Docker Desktop on Windows
	"docker-proxy",           // Linux userland proxy
	"docker-pr",              // docker-proxy through lsof
	"rootlesskit",            // rootless Docker
	"rootlessport",           // rootless Docker
	"slirp4netns",            // rootless Docker
	"/usr/bin/docker-proxy",  // a path rather than a bare name
	`C:\Docker\vpnkit.exe`,   // a Windows path
}

// nativeSpellings are the names issue #95 is about, plus the near-misses that
// must not be mistaken for a forwarder.
var nativeSpellings = []string{
	"node", // the native processes holding a stale container's port
	"python3",
	"postgres",
	"docker",              // the CLI never holds a published port
	"dockerd",             // neither does the engine on Linux
	"com.docker.backendx", // longer than a real name is not a truncation of it
	"vpn",                 // shorter than minTruncatedName
	"docker-",             // also under minTruncatedName
	"",
}

func TestIsForwarderProcess(t *testing.T) {
	for _, spelling := range forwarderSpellings {
		t.Run("forwarder/"+spelling, func(t *testing.T) {
			if !isForwarderProcess(spelling) {
				t.Errorf("isForwarderProcess(%q) = false, want true", spelling)
			}
		})
	}
	for _, spelling := range nativeSpellings {
		t.Run("native/"+spelling, func(t *testing.T) {
			if isForwarderProcess(spelling) {
				t.Errorf("isForwarderProcess(%q) = true, want false", spelling)
			}
		})
	}
}

// TestHeldByForwarder pins which observation the decision rests on: a reported
// process name outranks a command line, and an owner nobody named is not
// evidence against the cached list. The decision must not rewrite the row.
func TestHeldByForwarder(t *testing.T) {
	fixtures := []struct {
		label   string
		process string
		command string
		pid     int
		want    bool
	}{
		{label: "a forwarder reported by name", process: "com.docke", want: true},
		{label: "a native process reported by name", process: "node", want: false},
		// The name is the scanner's direct observation, so it outranks a
		// command line that happens to mention a forwarder.
		{label: "a name beats a misleading command", process: "node", command: "/usr/bin/docker-proxy -proto tcp", want: false},
		{label: "a forwarder only in the command", command: "/Applications/Docker.app/Contents/MacOS/com.docker.backend services", want: true},
		{label: "a quoted Windows command", command: `"C:\Program Files\Docker\Docker\resources\com.docker.backend.exe" --x`, want: true},
		{label: "a native process only in the command", command: "node server.js", want: false},
		// Windows' netstat reports pids without names before ports.Enrich runs.
		// Nothing named the owner, so there is no evidence against the cached
		// list and it keeps applying.
		{label: "an owner nobody named", pid: 4242, want: true},
	}
	for _, fx := range fixtures {
		t.Run(fx.label, func(t *testing.T) {
			row := ports.ListeningPort{Process: fx.process, Command: fx.command, PID: fx.pid}
			beforeProcess, beforeCommand, beforePID := row.Process, row.Command, row.PID
			if got := heldByForwarder(&row); got != fx.want {
				t.Errorf("heldByForwarder(%+v) = %v, want %v", row, got, fx.want)
			}
			if row.Process != beforeProcess || row.Command != beforeCommand || row.PID != beforePID {
				t.Errorf("heldByForwarder mutated the row: %+v", row)
			}
		})
	}
}

// TestEnrichSkipsAPortANativeProcessHolds is issue #95 at the enrichment step:
// the cached list still says itest-web-1 publishes 3000, while node holds it.
func TestEnrichSkipsAPortANativeProcessHolds(t *testing.T) {
	containers := parsePS([]byte("itest-web-1\tnginx:itest\t0.0.0.0:3000->80/tcp\tweb\titestproj\t/src/app\n"))

	native := []ports.ListeningPort{{Port: 3000, PID: 501, Process: "node", Type: ports.PortTypeUser}}
	enrichFrom(native, containers)
	assertUnstamped(t, native[0])

	forwarded := []ports.ListeningPort{{Port: 3000, PID: 77, Process: "com.docke"}}
	enrichFrom(forwarded, containers)
	if forwarded[0].Type != ports.PortTypeDocker || forwarded[0].DockerContainer != "itest-web-1" ||
		forwarded[0].DockerImage != "nginx:itest" || forwarded[0].DockerComposeService != "web" ||
		forwarded[0].DockerComposeProject != "itestproj" ||
		forwarded[0].DockerComposeWorkingDir != "/src/app" || forwarded[0].DockerContainerPort != 80 {
		t.Errorf("the forwarder's port lost its container data: %+v", forwarded[0])
	}
}

// assertUnstamped fails when a row carries any container attribution at all.
func assertUnstamped(t *testing.T, row ports.ListeningPort) {
	t.Helper()
	if row.Type == ports.PortTypeDocker {
		t.Fatalf("row was marked as Docker's: %+v", row)
	}
	if row.DockerContainer != "" || row.DockerImage != "" || row.DockerContainerPort != 0 {
		t.Fatalf("row was stamped from the cached list: %+v", row)
	}
	if row.DockerComposeProject != "" || row.DockerComposeService != "" || row.DockerComposeWorkingDir != "" {
		t.Fatalf("row carries compose labels from the cached list: %+v", row)
	}
}
