package ports

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// lsof listens with `lsof -a -n -P +c 0 -iTCP -sTCP:LISTEN -Fpcltn`, whose
// field output is a record stream, not a table:
//
//	p994          pid that owns everything until the next p
//	crapportd     command name
//	f15           file descriptor
//	l             lock mode (blank when unlocked)
//	tIPv4         address family of the file below
//	n*:58748      the listening endpoint
//
// The family line is what makes this decode trustworthy: lsof prints the
// wildcard as `*` for both families, so `*` alone cannot say whether a row is
// 0.0.0.0 or ::. netstat prints a dual-stack socket as one tcp46 row; lsof
// prints it as one IPv4 file and one IPv6 file, which is the same fact in two
// rows and folds back together in mergeAddresses.
//
// This is the Darwin fallback for macOS 27 and later, where a process started
// by a non-Apple-signed parent can run netstat successfully and still get no
// TCP table (issue #156). It is not the primary collector because lsof walks
// files instead of reading the PCB and cannot see sockets owned by other users:
// a port held by root is absent from its table. See docs/cli.md.
func decodeDarwinLsof(text string) ([]ListeningPort, error) {
	var (
		pid     int
		process string
		family  string
		named   bool
		seen    = make(map[listenerKey]bool)
		out     []ListeningPort
	)
	var problems []string
	for row, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		value := line[1:]
		switch line[0] {
		case 'p':
			parsed, err := strconv.ParseUint(value, 10, 31)
			if err != nil {
				problems = append(problems, fmt.Sprintf("record %d: invalid pid", row+1))
				pid, process, named = 0, "", false
				continue
			}
			pid, process, named = int(parsed), "", true
		case 'c':
			process = value
		case 't':
			family = value
		case 'l', 'f':
			// Lock mode and descriptor carry nothing the row needs.
		case 'n':
			if family == "" {
				// lsof prints the address family on the record's own t line.
				// Without it a wildcard endpoint cannot be placed in 0.0.0.0 or
				// ::, and guessing would publish a wrong identity.
				problems = append(problems, fmt.Sprintf("record %d: endpoint %q before its address family", row+1, value))
				continue
			}
			port, address, ipVersion, ok := decodeLsofEndpoint(value, family)
			if !ok {
				problems = append(problems, fmt.Sprintf("record %d: invalid listening endpoint %q", row+1, value))
				continue
			}
			if !named {
				problems = append(problems, fmt.Sprintf("record %d: listening endpoint without an owner", row+1))
				continue
			}
			key := listenerKey{port, address}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, ListeningPort{
				Port: port, PID: pid, Process: process,
				BindAddress: address, IPVersion: ipVersion,
			})
		}
	}
	if !named && len(out) == 0 && strings.TrimSpace(text) != "" {
		// An empty capture is a legitimately empty table: lsof exits 1 with no
		// output when nothing matches, and the caller decides what that means.
		// Anything else without a process record is not a table this decoder
		// understands — a banner, a diagnostic, a changed format.
		return nil, observationf(CodeObservationUnrecognized, observationUnrecognizedHint,
			"lsof: response has no process record")
	}
	if len(problems) > 0 {
		return out, &ObservationError{
			Code:    CodeObservationIncomplete,
			Detail:  "lsof: unreadable listener rows: " + strings.Join(problems, "; "),
			Hint:    observationIncompleteHint,
			Partial: true,
			Rows:    out,
		}
	}
	return out, nil
}

// decodeLsofEndpoint reads one lsof NAME field into a port and bind address.
// Families come from the record's own t line: IPv6 literals are bracketed
// ("[::1]:5432") and the wildcard is printed as "*" in both families.
func decodeLsofEndpoint(name, family string) (port int, address, ipVersion string, ok bool) {
	if strings.Contains(name, "->") {
		return 0, "", "", false
	}
	colon := strings.LastIndexByte(name, ':')
	if colon <= 0 {
		return 0, "", "", false
	}
	parsed, err := strconv.ParseUint(name[colon+1:], 10, 16)
	if err != nil || parsed == 0 {
		return 0, "", "", false
	}
	v6 := family == "IPv6"
	address, ipVersion = normalizeBind(name[:colon], v6)
	if _, err := netip.ParseAddr(address); err != nil {
		return 0, "", "", false
	}
	return int(parsed), address, ipVersion, true
}
