package ports

import (
	"net/netip"
	"strconv"
	"strings"
)

type listenerFormat uint8

const (
	lsofListeners listenerFormat = iota
	ssListeners
	netstatListeners
	darwinNetstatListeners
)

// socketRecord contains only acquired facts. The much wider ListeningPort
// contract is allocated exactly once after decoding, not grown for every row.
type socketRecord struct {
	port, pid                   int
	process, user, bind, family string
}

type listenerKey struct {
	port int
	bind string
}

func parseLsof(text string) []ListeningPort    { return decodeListeners(text, lsofListeners) }
func parseSS(text string) []ListeningPort      { return decodeListeners(text, ssListeners) }
func parseNetstat(text string) []ListeningPort { return decodeListeners(text, netstatListeners) }

// decodeListeners accepts headers, blank preambles and headerless captures.
// Each malformed row is local to that row; a long diagnostic never truncates
// all subsequent observations. The first observation of a socket wins.
func decodeListeners(text string, format listenerFormat) []ListeningPort {
	if format == darwinNetstatListeners {
		rows, _ := decodeDarwinNetstat(text)
		return rows
	}
	seen := make(map[listenerKey]struct{})
	var records []socketRecord
	for text != "" {
		line, rest, _ := strings.Cut(text, "\n")
		text = rest
		record, ok := decodeSocket(line, format)
		if !ok {
			continue
		}
		key := listenerKey{record.port, record.bind}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil
	}
	out := make([]ListeningPort, len(records))
	for i, record := range records {
		// Detach one row-sized text block, not three small strings and not the
		// entire command output. The builder is never reused after String.
		var text strings.Builder
		processEnd := len(record.process)
		userEnd := processEnd + len(record.user)
		text.Grow(userEnd + len(record.bind))
		text.WriteString(record.process)
		text.WriteString(record.user)
		text.WriteString(record.bind)
		detached := text.String()
		out[i] = ListeningPort{
			Port: record.port, PID: record.pid,
			Process: detached[:processEnd], User: detached[processEnd:userEnd],
			BindAddress: detached[userEnd:], IPVersion: record.family,
		}
	}
	return out
}

// takeListenerFields reads fixed table columns into caller-owned storage.
// Quoted ss process data is decoded separately from the original line.
func takeListenerFields(line string, fields *[9]string) (count int, rest string) {
	for count < len(fields) {
		start := 0
		for start < len(line) && line[start] <= ' ' {
			start++
		}
		if start == len(line) {
			return count, ""
		}
		end := start
		for end < len(line) && line[end] > ' ' {
			end++
		}
		fields[count] = line[start:end]
		count++
		line = line[end:]
	}
	return count, line
}

func decodeSocket(line string, format listenerFormat) (socketRecord, bool) {
	var columns [9]string
	count, rest := takeListenerFields(line, &columns)
	var record socketRecord
	var endpoint string
	var v6 bool
	switch format {
	case lsofListeners:
		if count < 9 || columns[7] != "TCP" || (columns[4] != "IPv4" && columns[4] != "IPv6") {
			return record, false
		}
		if tail := strings.TrimSpace(rest); tail != "" && !strings.HasPrefix(tail, "(LISTEN)") {
			return record, false
		}
		pid, err := strconv.Atoi(columns[1])
		if err != nil || pid < 0 {
			return record, false
		}
		record.pid, record.process, record.user = pid, columns[0], columns[2]
		endpoint, v6 = columns[8], columns[4] == "IPv6"
	case netstatListeners:
		if count < 5 || (!strings.EqualFold(columns[0], "TCP") && !strings.EqualFold(columns[0], "TCPV6")) || !strings.EqualFold(columns[3], "LISTENING") {
			return record, false
		}
		pid, err := strconv.Atoi(columns[4])
		if err != nil || pid < 0 {
			return record, false
		}
		record.pid = pid
		endpoint, v6 = columns[1], strings.EqualFold(columns[0], "TCPV6")
	case ssListeners:
		if count >= 5 && columns[0] == "LISTEN" {
			endpoint = columns[3]
		} else if count >= 6 && (columns[0] == "tcp" || columns[0] == "tcp6") && columns[1] == "LISTEN" {
			endpoint, v6 = columns[4], columns[0] == "tcp6"
		} else {
			return record, false
		}
		record.pid, record.process = parseSSUsers(line)
	default:
		return record, false
	}
	colon := strings.LastIndexByte(endpoint, ':')
	if colon < 0 || strings.Contains(endpoint, "->") {
		return socketRecord{}, false
	}
	port, err := strconv.ParseUint(endpoint[colon+1:], 10, 16)
	if err != nil || port == 0 {
		return socketRecord{}, false
	}
	address := endpoint[:colon]
	if address == "" || address == "[]" {
		return socketRecord{}, false
	}
	if strings.HasPrefix(address, "[") != strings.HasSuffix(address, "]") {
		return socketRecord{}, false
	}
	record.port = int(port)
	record.bind, record.family = normalizeBind(address, v6)
	if _, err := netip.ParseAddr(record.bind); err != nil {
		return socketRecord{}, false
	}
	return record, true
}

// parseSSUsers reads exactly the first quoted process record. Names containing
// spaces, quotes or the text pid= cannot change which token supplies the PID.
func parseSSUsers(line string) (int, string) {
	marker := strings.Index(line, `users:(("`)
	if marker < 0 {
		return 0, ""
	}
	quoted := line[marker+len("users:(("):]
	end := 1
	for end < len(quoted) {
		if quoted[end] == '\\' {
			end += 2
			continue
		}
		if quoted[end] == '"' {
			break
		}
		end++
	}
	if end >= len(quoted)-1 || quoted[end+1] != ',' {
		return 0, ""
	}
	name := quoted[1:end]
	if strings.Contains(name, `\`) {
		if decoded, err := strconv.Unquote(quoted[:end+1]); err == nil {
			name = decoded
		}
	}
	tail := strings.TrimSpace(quoted[end+2:])
	tail = strings.TrimPrefix(tail, "pid=")
	stop := strings.IndexAny(tail, ",)")
	if stop < 0 {
		return 0, name
	}
	pid, err := strconv.Atoi(strings.TrimSpace(tail[:stop]))
	if err != nil || pid < 0 {
		return 0, name
	}
	return pid, name
}

// normalizeBind keeps wildcard family explicit while preserving literal IPv6
// and zone identifiers. Numeric address text is never resolved through DNS.
func normalizeBind(raw string, v6 bool) (string, string) {
	address := strings.TrimSpace(raw)
	if len(address) >= 2 && address[0] == '[' && address[len(address)-1] == ']' {
		address = address[1 : len(address)-1]
	}
	if address == "" || address == "*" {
		if v6 {
			return "::", "IPv6"
		}
		return "0.0.0.0", "IPv4"
	}
	if strings.ContainsRune(address, ':') {
		return address, "IPv6"
	}
	return address, "IPv4"
}

// mergeAddresses projects socket observations to one row per process/port,
// preserving primary observation order without aliasing input address slices.
func mergeAddresses(rows []ListeningPort) []ListeningPort {
	type processPort struct{ pid, port int }
	positions := make(map[processPort]int, len(rows))
	out := make([]ListeningPort, 0, len(rows))
	for _, row := range rows {
		key := processPort{row.PID, row.Port}
		position, exists := positions[key]
		if !exists {
			row.BindAddresses = append([]string(nil), row.BindAddresses...)
			positions[key] = len(out)
			out = append(out, row)
			continue
		}
		addAddress := func(address string) {
			if address == out[position].BindAddress {
				return
			}
			for _, previous := range out[position].BindAddresses {
				if address == previous {
					return
				}
			}
			out[position].BindAddresses = append(out[position].BindAddresses, address)
		}
		addAddress(row.BindAddress)
		for _, address := range row.BindAddresses {
			addAddress(address)
		}
	}
	return out
}
