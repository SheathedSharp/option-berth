package docker

import (
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// Docker never hands a host port to a container: a helper on the host holds the
// listening socket and forwards what arrives. That helper is the only honest
// sign — without asking Docker anything — that a host port is really a
// container's. It is what lets the runtime service tell a stale container claim from a
// live one while Docker is wedged and the cached container list cannot be
// refreshed.
//
// The names below are what a port scanner reports as the process: lsof's
// COMMAND on macOS, ss's comm on Linux, tasklist's image name on Windows. Both
// lsof (9 characters by default) and comm (15) truncate, so a reported name is
// also accepted as a prefix of a longer forwarder name.
// forwarderNames is every process name that means "Docker is holding this
// port", grouped by the deployment it belongs to.
var forwarderNames = map[string][]string{
	// Docker Desktop's backend holds every published port on macOS and Windows.
	// Measured with Docker Engine 29.2.1: a container published as
	// `-p 47311:80` is held by
	// /Applications/Docker.app/Contents/MacOS/com.docker.backend, which lsof
	// reports as the truncated "com.docke".
	"desktop": {"com.docker.backend"},
	// Older Docker Desktop builds forwarded through vpnkit, either as its own
	// binary or under the com.docker prefix.
	"vpnkit": {"vpnkit", "com.docker.vpnkit", "com.docker.proxy"},
	// The Linux engine's userland proxy. With `userland-proxy: false` nothing
	// listens on the host at all — the port is DNAT'd — so such a port never
	// reaches a scan and never needs this check.
	"engine": {"docker-proxy"},
	// Rootless Docker forwards through RootlessKit's port driver instead.
	"rootless": {"rootlesskit", "rootlessport", "slirp4netns"},
}

// forwarders is the flattened name list, built once.
var forwarders = flattenForwarders()

func flattenForwarders() []string {
	var names []string
	for _, group := range forwarderNames {
		names = append(names, group...)
	}
	return names
}

// minForwarderPrefix is the shortest reported name accepted as a truncation.
// lsof cuts at 9 characters, so no real report is shorter than that; requiring
// 8 keeps a short native process name from matching a longer forwarder by
// accident.
const minForwarderPrefix = 8

// heldByForwarder reports whether the cached container list may speak for this
// port: true when one of Docker's forwarders holds it, false when something
// else does.
//
// A port whose owner the scan could not name at all is not evidence either way,
// so the list still applies to it. That is the Windows case: `netstat -ano`
// reports pids without names, and ports.Enrich fills the names in only after
// this pass, so the check is inert there and behaves as it did before it
// existed.
func heldByForwarder(row *ports.ListeningPort) bool {
	owner := row.Process
	if owner == "" {
		owner = firstWord(row.Command)
	}
	if owner == "" {
		return true
	}
	return isForwarderProcess(owner)
}

// isForwarderProcess reports whether a scanner-reported process name belongs to
// one of Docker's port forwarders, allowing for the truncation scanners apply.
func isForwarderProcess(reported string) bool {
	name := normalizeProcess(reported)
	if name == "" {
		return false
	}
	for _, forwarder := range forwarders {
		if sameForwarder(name, forwarder) {
			return true
		}
	}
	return false
}

// sameForwarder compares one reported name with one forwarder name, accepting a
// truncation of the latter.
func sameForwarder(reported, forwarder string) bool {
	switch {
	case reported == forwarder:
		return true
	case len(reported) < minForwarderPrefix || len(reported) >= len(forwarder):
		return false
	}
	return strings.HasPrefix(forwarder, reported)
}

// normalizeProcess reduces a scanner's process name to something comparable:
// lower case, no directory, no Windows executable suffix.
func normalizeProcess(reported string) string {
	name := strings.ToLower(strings.TrimSpace(reported))
	if at := strings.LastIndexAny(name, `/:\`); at >= 0 {
		name = name[at+1:]
	}
	return strings.TrimSuffix(name, ".exe")
}

// firstWord is the executable of a command line, used only when the scanner
// reported no process name of its own. A quoted executable — a Windows path
// with spaces — is read up to its closing quote.
func firstWord(commandline string) string {
	rest := strings.TrimSpace(commandline)
	quoted, isQuoted := strings.CutPrefix(rest, `"`)
	if !isQuoted {
		if space := strings.IndexAny(rest, " \t"); space >= 0 {
			return rest[:space]
		}
		return rest
	}
	closing := strings.IndexByte(quoted, '"')
	if closing < 0 {
		return quoted
	}
	return quoted[:closing]
}
