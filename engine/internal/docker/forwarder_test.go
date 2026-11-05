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

func TestHeldByForwarder(t *testing.T) {
	cases := []struct {
		label string
		row   ports.ListeningPort
		want  bool
	}{
		{"a forwarder reported by name", ports.ListeningPort{Process: "com.docke"}, true},
		{"a native process reported by name", ports.ListeningPort{Process: "node"}, false},
		// The name is the scanner's direct observation, so it outranks a
		// command line that happens to mention a forwarder.
		{"a name beats a misleading command", ports.ListeningPort{Process: "node", Command: "/usr/bin/docker-proxy -proto tcp"}, false},
		{"a forwarder only in the command", ports.ListeningPort{Command: "/Applications/Docker.app/Contents/MacOS/com.docker.backend services"}, true},
		{"a quoted Windows command", ports.ListeningPort{Command: `"C:\Program Files\Docker\Docker\resources\com.docker.backend.exe" --x`}, true},
		{"a native process only in the command", ports.ListeningPort{Command: "node server.js"}, false},
		// Windows' netstat reports pids without names before ports.Enrich runs.
		// Nothing named the owner, so there is no evidence against the cached
		// list and it keeps applying.
		{"an owner nobody named", ports.ListeningPort{PID: 4242}, true},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			row := tc.row
			if got := heldByForwarder(&row); got != tc.want {
				t.Errorf("heldByForwarder(%+v) = %v, want %v", row, got, tc.want)
			}
		})
	}
}

// TestEnrichSkipsAPortANativeProcessHolds is issue #95 at the enrichment step:
// the cached list says itest-web-1 publishes 3000, but node holds 3000 now.
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
