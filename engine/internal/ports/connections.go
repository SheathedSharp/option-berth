package ports

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// applyConnectionCounts fills every row's established-connection count from a
// single listing of the machine's TCP connections.
//
// The per-port form ran one `lsof -iTCP:<port>` (or `ss`) for each listener.
// On a machine with three dozen listeners that was three dozen forks per scan,
// all inside the lock shared with the stats tick; one listing costs a fraction
// of that and answers for every port at once.
func applyConnectionCounts(pp []ListeningPort) {
	applyConnectionCountsContext(context.Background(), pp)
}

func applyConnectionCountsContext(ctx context.Context, pp []ListeningPort) {
	if len(pp) == 0 || ctx.Err() != nil {
		return
	}
	counts := connectionCountsContext(ctx)
	// No evidence is not an observed zero. A successful empty listing returns
	// a non-nil empty map and still clears previously observed connections.
	if counts == nil || ctx.Err() != nil {
		return
	}
	for i := range pp {
		pp[i].Connections = counts[pp[i].Port]
	}
}

// connectionCounts lists the machine's established TCP connections once and
// counts them per local port.
func connectionCounts() map[int]int {
	return connectionCountsContext(context.Background())
}

func connectionCountsContext(ctx context.Context) map[int]int {
	return connectionCountsFor(ctx, runtime.GOOS)
}

// The platform is explicit so empty-match exit handling can be tested without
// confusing a successful zero-connection listing with a command failure.
func connectionCountsFor(ctx context.Context, platform string) map[int]int {
	if ctx.Err() != nil {
		return nil
	}
	switch platform {
	case "windows":
		cmd, stop := boundedContext(ctx, "netstat", "-ano")
		defer stop()
		out, err := cmd.Output()
		if err != nil || ctx.Err() != nil {
			return nil
		}
		return countNetstatByPort(string(out))
	case "darwin":
		cmd, stop := boundedContext(ctx, "lsof", "-iTCP", "-sTCP:ESTABLISHED", "-n", "-P")
		defer stop()
		out, err := cmd.Output()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			// lsof exits 1 for an empty match. Only blank stdout AND stderr give
			// evidence of zero here; a warning, partial listing or other exit does not.
			var exited *exec.ExitError
			if errors.As(err, &exited) && exited.ExitCode() == 1 && len(bytes.TrimSpace(out)) == 0 && len(bytes.TrimSpace(exited.Stderr)) == 0 {
				return map[int]int{}
			}
			return nil
		}
		return countLsofByPort(string(out))
	default:
		cmd, stop := boundedContext(ctx, "ss", "-tn", "state", "established")
		defer stop()
		out, err := cmd.Output()
		if err != nil || ctx.Err() != nil {
			return nil
		}
		return countSSByPort(string(out))
	}
}

// countLsofByPort counts `lsof -iTCP -sTCP:ESTABLISHED -n -P` by port.
//
// It reports the same numbers the per-port `lsof -iTCP:<port>` calls did,
// including the halving: `-i:<port>` matches at either end, so a loopback pair
// appears twice — once as the server's fd, once as the client's — and the old
// code divided by two. Counting both ends here keeps the published figure
// unchanged.
func countLsofByPort(out string) map[int]int {
	endpointHits := map[int]int{}
	for _, line := range strings.Split(out, "\n") {
		server, client, ok := lsofEndpoints(line)
		if !ok {
			continue
		}
		endpointHits[server]++
		endpointHits[client]++
	}
	counts := make(map[int]int, len(endpointHits))
	for port, hits := range endpointHits {
		counts[port] = hits / 2
	}
	return counts
}

// lsofEndpoints pulls the two ports out of an lsof NAME field such as
// "127.0.0.1:3000->127.0.0.1:52344 (ESTABLISHED)", IPv6 brackets included.
func lsofEndpoints(line string) (local, remote int, ok bool) {
	for _, field := range strings.Fields(line) {
		left, right, paired := strings.Cut(field, "->")
		if !paired {
			continue
		}
		localPort, localOK := portAfterColon(left)
		remotePort, remoteOK := portAfterColon(right)
		if localOK && remoteOK {
			return localPort, remotePort, true
		}
	}
	return 0, 0, false
}

// countSSByPort counts `ss -tn state established` by local port. `ss` reports
// the local end as the source, which is what the per-port `sport = :<port>`
// filter selected, so nothing is halved.
//
// Columns are read from the right, not by index: `ss` keeps a State column
// when it is not filtering by state and drops it when it is, so a fixed index
// reads Send-Q on one machine and the local address on another. The last two
// address-shaped fields are always local and peer.
func countSSByPort(out string) map[int]int {
	counts := map[int]int{}
	for _, line := range strings.Split(out, "\n") {
		if local, ok := ssLocalPort(strings.Fields(line)); ok {
			counts[local]++
		}
	}
	return counts
}

// ssLocalPort is the local port of one `ss` row: the second-to-last field that
// looks like an address with a port.
func ssLocalPort(fields []string) (int, bool) {
	ports := make([]int, 0, 2)
	for _, field := range fields {
		if port, ok := portAfterColon(field); ok {
			ports = append(ports, port)
		}
	}
	if len(ports) >= 2 {
		return ports[len(ports)-2], true
	}
	return 0, false
}

// countNetstatByPort counts the ESTABLISHED rows of `netstat -ano` by local
// port.
func countNetstatByPort(out string) map[int]int {
	counts := map[int]int{}
	for _, line := range strings.Split(out, "\n") {
		columns := strings.Fields(strings.TrimSpace(line))
		if len(columns) < 4 || strings.ToUpper(columns[3]) != "ESTABLISHED" {
			continue
		}
		if port, ok := portAfterColon(columns[1]); ok {
			counts[port]++
		}
	}
	return counts
}

// portAfterColon reads the port off an address, ignoring IPv6 brackets and the
// colons inside them.
func portAfterColon(address string) (int, bool) {
	colon := strings.LastIndex(address, ":")
	if colon < 0 {
		return 0, false
	}
	port, err := strconv.Atoi(strings.TrimSpace(address[colon+1:]))
	if err != nil || port < 1 {
		return 0, false
	}
	return port, true
}
