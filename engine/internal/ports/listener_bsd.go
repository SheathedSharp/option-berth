package ports

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Darwin netstat reads TCP PCBs directly. Unlike lsof's file/device walk, an
// unrelated unreadable filesystem mount cannot make socket collection fail.
// Resolve PID columns from the header: macOS versions use either pid or
// process:pid, and may add traffic columns before it. No fixed PID offset.
//
// An unreadable row is kept out of the result, but it does not erase the rows
// that were readable: the error is marked Partial and carries them. A caller
// that publishes those rows must also publish the observation status, or an
// unreadable port would look absent. A headerless (usually sandbox-blocked)
// response is still a whole-observation failure.
func decodeDarwinNetstat(text string) ([]ListeningPort, error) {
	pidColumn, namedPID := -1, false
	seen := make(map[listenerKey]bool)
	var out []ListeningPort
	var problems []string
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
		if len(fields) == 0 {
			continue
		}
		proto := fields[0]
		if proto != "tcp4" && proto != "tcp6" && proto != "tcp46" {
			continue
		}
		if len(fields) < 6 {
			problems = append(problems, fmt.Sprintf("line %d: truncated TCP row", row+1))
			continue
		}
		if fields[5] != "LISTEN" {
			continue
		}
		if pidColumn < 0 || pidColumn >= len(fields) {
			problems = append(problems, fmt.Sprintf("line %d: missing TCP process identity column", row+1))
			continue
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
				problems = append(problems, fmt.Sprintf("line %d: unreadable TCP process identity", row+1))
				continue
			}
		}
		pid, err := strconv.ParseUint(pidText, 10, 31)
		if err != nil {
			problems = append(problems, fmt.Sprintf("line %d: invalid TCP process identity", row+1))
			continue
		}
		endpoint := fields[3]
		dot := strings.LastIndexByte(endpoint, '.')
		if dot <= 0 {
			problems = append(problems, fmt.Sprintf("line %d: invalid TCP endpoint %q", row+1, endpoint))
			continue
		}
		port, err := strconv.ParseUint(endpoint[dot+1:], 10, 16)
		if err != nil || port == 0 {
			problems = append(problems, fmt.Sprintf("line %d: invalid listening port in %q", row+1, endpoint))
			continue
		}
		badAddress := false
		for _, bind := range darwinBinds(proto, endpoint[:dot]) {
			if _, err := netip.ParseAddr(bind.address); err != nil {
				problems = append(problems, fmt.Sprintf("line %d: invalid listening address %q", row+1, endpoint))
				badAddress = true
				break
			}
			key := listenerKey{int(port), bind.address}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, ListeningPort{
				Port: int(port), PID: int(pid), Process: process,
				BindAddress: strings.Clone(bind.address), IPVersion: bind.family,
			})
		}
		if badAddress {
			continue
		}
	}
	if pidColumn < 0 {
		if strings.TrimSpace(text) == "" {
			return nil, observationf(CodeObservationEmpty, observationEmptyHint,
				"netstat: response was empty")
		}
		return nil, observationf(CodeObservationUnrecognized, observationUnrecognizedHint,
			"netstat: response has no recognized TCP table header")
	}
	if len(problems) > 0 {
		return out, &ObservationError{
			Code:    CodeObservationIncomplete,
			Detail:  "netstat: unreadable TCP rows: " + strings.Join(problems, "; "),
			Hint:    observationIncompleteHint,
			Partial: true,
			Rows:    out,
		}
	}
	return out, nil
}

type darwinBind struct {
	address string
	family  string
}

// darwinBinds maps the protocol label netstat prints to the addresses the
// socket holds. tcp46 is a dual-stack socket: macOS shows one row, but the
// same socket answers on both families. Emitting both wildcard addresses lets
// mergeAddresses fold them into one row with the second in BindAddresses;
// treating tcp46 as uninteresting made every Go/Node/Java default bind vanish
// from status, readiness and dependency waits.
func darwinBinds(proto, raw string) []darwinBind {
	if proto != "tcp46" {
		bind, family := normalizeBind(raw, proto == "tcp6")
		return []darwinBind{{address: bind, family: family}}
	}
	v4, family4 := normalizeBind(raw, false)
	v6, family6 := normalizeBind(raw, true)
	binds := []darwinBind{{address: v4, family: family4}}
	if v6 != v4 {
		binds = append(binds, darwinBind{address: v6, family: family6})
	}
	return binds
}
