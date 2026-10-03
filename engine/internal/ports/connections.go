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

// applyConnectionCounts fills in every row's established-connection count from
// a single listing of the machine's TCP connections.
//
// It used to be one `lsof -iTCP:<port>` (or one `ss`) per listening port. On a
// developer machine with three dozen listeners that is three dozen forks per
// scan and, measured on macOS, 2.4 s of the 4.5 s a stats-enabled scan took —
// all of it inside the lock that keeps scans and the stats tick from
// interleaving, which starved the 1 s tick down to a delta every five or six
// seconds. One listing costs 75 ms and answers for every port at once.
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

// connectionCounts lists established TCP connections once and counts them per
// local port.
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
// It reproduces exactly what the per-port `lsof -iTCP:<port>` calls reported,
// halving included: `-i:<port>` matches a connection at *either* end, so a
// loopback connection between two local processes showed up twice — once as
// the server's fd and once as the client's — and the old code divided by two
// to undo that. Both endpoints are counted here for the same reason, so the
// published `connections` figure does not move for anyone.
func countLsofByPort(out string) map[int]int {
	hits := map[int]int{}
	for _, line := range strings.Split(out, "\n") {
		local, remote, ok := lsofEndpoints(line)
		if !ok {
			continue
		}
		hits[local]++
		hits[remote]++
	}
	counts := make(map[int]int, len(hits))
	for port, n := range hits {
		counts[port] = n / 2
	}
	return counts
}

// lsofEndpoints pulls the two port numbers out of an lsof NAME field of the
// form "127.0.0.1:3000->127.0.0.1:52344 (ESTABLISHED)", IPv6 brackets and all.
func lsofEndpoints(line string) (local, remote int, ok bool) {
	for _, field := range strings.Fields(line) {
		arrow := strings.Index(field, "->")
		if arrow < 0 {
			continue
		}
		l, lok := portAfterColon(field[:arrow])
		r, rok := portAfterColon(field[arrow+2:])
		if lok && rok {
			return l, r, true
		}
	}
	return 0, 0, false
}

// countSSByPort counts `ss -tn state established` by local port. `ss` reports
// only the local end as the source, which is what the per-port
// `sport = :<port>` filter selected, so no halving applies.
//
// The columns are read from the right, not by index: `ss` prints a State
// column when it is not filtering by state and drops it when it is, so a
// fixed index reads Send-Q on one machine and the local address on another.
// The last two address-shaped fields are always local and peer.
func countSSByPort(out string) map[int]int {
	counts := map[int]int{}
	for _, line := range strings.Split(out, "\n") {
		local, ok := ssLocalPort(strings.Fields(line))
		if !ok {
			continue
		}
		counts[local]++
	}
	return counts
}

// ssLocalPort returns the local port of one `ss` row: the second-to-last field
// that looks like an address with a port.
func ssLocalPort(fields []string) (int, bool) {
	var ports []int
	for _, f := range fields {
		if p, ok := portAfterColon(f); ok {
			ports = append(ports, p)
		}
	}
	if len(ports) < 2 {
		return 0, false
	}
	return ports[len(ports)-2], true
}

// countNetstatByPort counts `netstat -ano` ESTABLISHED rows by local port.
func countNetstatByPort(out string) map[int]int {
	counts := map[int]int{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 4 || strings.ToUpper(fields[3]) != "ESTABLISHED" {
			continue
		}
		if port, ok := portAfterColon(fields[1]); ok {
			counts[port]++
		}
	}
	return counts
}

// portAfterColon reads the port off an address, ignoring the IPv6 brackets and
// the colons inside them.
func portAfterColon(addr string) (int, bool) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return 0, false
	}
	port, err := strconv.Atoi(strings.TrimSpace(addr[i+1:]))
	if err != nil || port <= 0 {
		return 0, false
	}
	return port, true
}
