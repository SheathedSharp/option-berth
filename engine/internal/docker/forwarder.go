package docker

import (
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// Docker does not hand a host port to the container: a helper process on the
// host holds the listening socket and forwards what arrives into the VM or the
// container's network namespace. That helper is the only honest sign, without
// asking Docker anything, that a host port really is a container's — which is
// what step 5A.9 needs, because the check has to hold while Docker is wedged
// and the cached container list cannot be refreshed (issue #95).
//
// The names are what a port scanner reports as the process: lsof's COMMAND on
// macOS, ss's comm on Linux, tasklist's image name on Windows. Both lsof and
// comm truncate (lsof to 9 characters by default, comm to 15), so a name is
// also matched as a prefix of a full name — see forwarderProcess.
var forwarderNames = []string{
	// Docker Desktop's backend holds every published port on macOS and
	// Windows; it is `com.docker.backend.exe` there. Verified on macOS with
	// Docker Engine 29.2.1 (Docker Desktop): a container published with `-p 47311:80`
	// is held by /Applications/Docker.app/Contents/MacOS/com.docker.backend,
	// which lsof reports as the truncated "com.docke".
	"com.docker.backend",
	// Older Docker Desktop builds forwarded through vpnkit, either as its own
	// binary or under the com.docker prefix.
	"vpnkit",
	"com.docker.vpnkit",
	"com.docker.proxy",
	// The Linux engine's userland proxy. With `userland-proxy: false` nothing
	// listens on the host at all — the port is DNAT'd — so such a port never
	// reaches a scan and never needs this check.
	"docker-proxy",
	// Rootless Docker forwards through RootlessKit's port driver instead.
	"rootlesskit",
	"rootlessport",
	"slirp4netns",
}

// minTruncatedName is the shortest prefix accepted as a truncated forwarder
// name. lsof truncates to 9 characters by default, so every name a scanner can
// report is at least that long; requiring 8 keeps a short native process name
// from matching one of the longer forwarders by accident.
const minTruncatedName = 8

// heldByForwarder reports whether the cached container list may speak for this
// port: true when Docker's own forwarder holds it, false when some other
// process does.
//
// A port whose owner the scan could not name at all is not evidence either
// way, so the container list still applies to it. That is the case on Windows,
// where `netstat -ano` reports pids without names and ports.Enrich only fills
// the names in after this pass — the check is inert there, and the behaviour
// is exactly what it was before 5A.9.
func heldByForwarder(p *ports.ListeningPort) bool {
	reported := p.Process
	if reported == "" {
		reported = firstWord(p.Command)
	}
	if reported == "" {
		// Nothing named the owner, so the container list is not contradicted.
		return true
	}
	return isForwarderProcess(reported)
}

// isForwarderProcess reports whether a scanner-reported process name belongs to
// one of Docker's port forwarders, allowing for the truncation lsof and comm
// apply to it.
func isForwarderProcess(reported string) bool {
	candidate := normalizeProcess(reported)
	if candidate == "" {
		return false
	}
	for _, forwarder := range forwarderNames {
		if matchesForwarder(candidate, forwarder) {
			return true
		}
	}
	return false
}

// matchesForwarder compares one reported name against one forwarder, accepting
// a truncation of it: a name at least minTruncatedName long that the forwarder
// starts with. The length floor is what stops a short native process from
// matching a longer forwarder by accident.
func matchesForwarder(candidate, forwarder string) bool {
	if candidate == forwarder {
		return true
	}
	truncated := len(candidate) >= minTruncatedName && len(candidate) < len(forwarder)
	return truncated && strings.HasPrefix(forwarder, candidate)
}

// normalizeProcess reduces a scanner's process name to something comparable:
// lower case, no directory, no Windows executable suffix.
func normalizeProcess(reported string) string {
	cleaned := strings.ToLower(strings.TrimSpace(reported))
	if separator := strings.LastIndexAny(cleaned, `/:\`); separator >= 0 {
		cleaned = cleaned[separator+1:]
	}
	return strings.TrimSuffix(cleaned, ".exe")
}

// firstWord is the executable of a command line, used only when the scanner
// reported no process name of its own. A quoted executable — a Windows path
// with spaces in it — is read up to its closing quote.
func firstWord(commandline string) string {
	remainder := strings.TrimSpace(commandline)
	if quoted, isQuoted := strings.CutPrefix(remainder, `"`); isQuoted {
		if closing := strings.IndexByte(quoted, '"'); closing >= 0 {
			return quoted[:closing]
		}
		return quoted
	}
	if end := strings.IndexAny(remainder, " \t"); end >= 0 {
		return remainder[:end]
	}
	return remainder
}
