package ports

import (
	"net/netip"
	"strconv"
	"strings"
)

// Darwin netstat reads TCP PCBs directly. Unlike lsof's file/device walk, an
// unrelated unreadable filesystem mount cannot make socket collection fail.
// Resolve PID columns from the header: macOS versions use either pid or
// process:pid, and may add traffic columns before it. No fixed PID offset.
//
// A malformed row invalidates the whole table instead of shrinking it: a
// caller that believes a port is free must not have been shown a prefix. Both
// that case and a headerless (usually sandbox-blocked) response are
// ObservationError values so the CLI can report a code and a next step.
func decodeDarwinNetstat(text string) ([]ListeningPort, error) {
	pidColumn, namedPID := -1, false
	seen := make(map[listenerKey]bool)
	var out []ListeningPort
	for row, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Proto ") {
			header := strings.NewReplacer("Local Address", "Local-Address", "Foreign Address", "Foreign-Address").Replace(line)
			for i, field := range strings.Fields(header) {
				if field == "pid" || field == "process:pid" {
					pidColumn, namedPID = i, field == "process:pid"
					break
				}
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 || (fields[0] != "tcp4" && fields[0] != "tcp6") {
			continue
		}
		if len(fields) < 6 {
			return nil, observationf(CodeObservationIncomplete, observationIncompleteHint,
				"netstat: truncated TCP row at line %d", row+1)
		}
		if fields[5] != "LISTEN" {
			continue
		}
		if pidColumn < 0 || pidColumn >= len(fields) {
			return nil, observationf(CodeObservationIncomplete, observationIncompleteHint,
				"netstat: missing TCP process identity column at line %d", row+1)
		}
		pidText, process := fields[pidColumn], ""
		if namedPID {
			found := false
			for i := pidColumn; i < len(fields); i++ {
				at := strings.LastIndexByte(fields[i], ':')
				if at < 0 {
					continue
				}
				if _, err := strconv.ParseUint(fields[i][at+1:], 10, 31); err != nil {
					continue
				}
				process = strings.Join(append(append([]string(nil), fields[pidColumn:i]...), fields[i][:at]), " ")
				pidText, found = fields[i][at+1:], true
				break
			}
			if !found {
				return nil, observationf(CodeObservationIncomplete, observationIncompleteHint,
					"netstat: unreadable TCP process identity at line %d", row+1)
			}
		}
		pid, err := strconv.ParseUint(pidText, 10, 31)
		if err != nil {
			return nil, observationf(CodeObservationIncomplete, observationIncompleteHint,
				"netstat: invalid TCP process identity at line %d", row+1)
		}
		endpoint := fields[3]
		dot := strings.LastIndexByte(endpoint, '.')
		if dot <= 0 {
			return nil, observationf(CodeObservationIncomplete, observationIncompleteHint,
				"netstat: invalid TCP endpoint at line %d", row+1)
		}
		port, err := strconv.ParseUint(endpoint[dot+1:], 10, 16)
		if err != nil || port == 0 {
			return nil, observationf(CodeObservationIncomplete, observationIncompleteHint,
				"netstat: invalid listening port at line %d", row+1)
		}
		bind, family := normalizeBind(endpoint[:dot], fields[0] == "tcp6")
		if _, err := netip.ParseAddr(bind); err != nil {
			return nil, observationf(CodeObservationIncomplete, observationIncompleteHint,
				"netstat: invalid listening address at line %d", row+1)
		}
		key := listenerKey{int(port), bind}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ListeningPort{Port: int(port), PID: int(pid), Process: process, BindAddress: strings.Clone(bind), IPVersion: family})
	}
	if pidColumn < 0 {
		return nil, observationf(CodeObservationEmpty, observationEmptyHint,
			"netstat: response has no recognized TCP table header")
	}
	return out, nil
}
