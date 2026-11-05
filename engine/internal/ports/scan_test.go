package ports

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseLsofMultipleBindsSamePort(t *testing.T) {
	output := `COMMAND   PID  USER   FD   TYPE   DEVICE SIZE/OFF NODE NAME
edge    23456 root    6u  IPv4  2234567      0t0  TCP 10.20.30.5:8081 (LISTEN)
edge    23456 root    7u  IPv4  2234568      0t0  TCP 10.20.31.5:8081 (LISTEN)
edge    23456 root    8u  IPv4  2234569      0t0  TCP 10.20.32.5:8443 (LISTEN)
edge    23456 root    9u  IPv4  2234570      0t0  TCP 10.20.32.5:8081 (LISTEN)
`
	results := parseLsof(output)
	if len(results) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(results))
	}

	binds := map[string]bool{}
	for _, result := range results {
		binds[result.BindAddress+":"+itoa(result.Port)] = true
	}
	for _, want := range []string{"10.20.30.5:8081", "10.20.31.5:8081", "10.20.32.5:8443", "10.20.32.5:8081"} {
		if !binds[want] {
			t.Errorf("expected bind %s missing from results", want)
		}
	}
}

func TestParseLsofDedupsSamePortAndBind(t *testing.T) {
	output := `COMMAND   PID  USER   FD   TYPE   DEVICE SIZE/OFF NODE NAME
app     4321 dev     6u  IPv4  3234567      0t0  TCP *:4100 (LISTEN)
app     4321 dev     7u  IPv4  3234568      0t0  TCP *:4100 (LISTEN)
`
	results := parseLsof(output)
	if len(results) != 1 {
		t.Fatalf("expected 1 deduped entry, got %d", len(results))
	}
	if results[0].BindAddress != "0.0.0.0" {
		t.Errorf("bind = %s, want 0.0.0.0", results[0].BindAddress)
	}
}

func TestParseSSProcessNameWithSpaces(t *testing.T) {
	// Next.js sets process.title to "next-server (v16.2.6)"; Linux truncates
	// comm to 15 bytes, so ss -p emits a quoted name containing a space.
	// strings.Fields must not be used to parse that blob.
	output := `State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process
LISTEN 0      511    *:4111             *:*    users:(("next-server (v1",pid=223211,fd=22))
LISTEN 0      128    127.0.0.1:4100     0.0.0.0:* users:(("app",pid=4321,fd=18))
LISTEN 0      128    *:8180             *:*
`
	rows := parseSS(output)
	if len(rows) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(rows))
	}

	byNumber := map[int]ListeningPort{}
	for _, result := range rows {
		byNumber[result.Port] = result
	}

	next := byNumber[4111]
	if next.PID != 223211 || next.Process != "next-server (v1" || next.BindAddress != "0.0.0.0" {
		t.Errorf("port 4111 = %+v", next)
	}
	app := byNumber[4100]
	if app.PID != 4321 || app.Process != "app" {
		t.Errorf("port 4100 = %+v", app)
	}
	idle := byNumber[8180]
	if idle.PID != 0 || idle.Process != "" {
		t.Errorf("port 8180 = pid %d name %q, want empty users blob", idle.PID, idle.Process)
	}
}

func TestParseSSUsersBlobVariants(t *testing.T) {
	cases := []struct {
		label   string
		line    string
		process string
		pid     int
	}{
		{
			label:   "one entry per file descriptor",
			line:    `LISTEN 0      128    0.0.0.0:8081    0.0.0.0:*    users:(("edge",pid=2345,fd=6),("edge",pid=2346,fd=6))`,
			process: "edge",
			pid:     2345,
		},
		{
			// A process title may contain "pid="; the real pid is the one
			// after the closing quote.
			label:   "name containing pid=",
			line:    `LISTEN 0      511    *:4111        *:*          users:(("worker pid=7",pid=99002,fd=22))`,
			process: "worker pid=7",
			pid:     99002,
		},
		{
			label:   "iproute2 without a pid label",
			line:    `LISTEN 0      128    0.0.0.0:22    0.0.0.0:*    users:(("sshd",654,3))`,
			process: "sshd",
			pid:     654,
		},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			output := "State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n" + tc.line + "\n"
			results := parseSS(output)
			if len(results) != 1 {
				t.Fatalf("expected 1 entry, got %d", len(results))
			}
			if results[0].Process != tc.process || results[0].PID != tc.pid {
				t.Errorf("process/pid = %q/%d, want %q/%d",
					results[0].Process, results[0].PID, tc.process, tc.pid)
			}
		})
	}
}

func TestParseSSMultipleBindsSamePort(t *testing.T) {
	output := `State  Recv-Q Send-Q Local Address:Port  Peer Address:Port Process
LISTEN 0      128    127.0.1.2:8181       0.0.0.0:*     users:(("nc",pid=1101,fd=3))
LISTEN 0      128    127.0.1.3:8181       0.0.0.0:*     users:(("nc",pid=1102,fd=3))
LISTEN 0      128    127.0.1.4:8181       0.0.0.0:*     users:(("nc",pid=1103,fd=3))
`
	rows := parseSS(output)
	if len(rows) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(rows))
	}
	addresses := map[string]bool{}
	for _, result := range rows {
		addresses[result.BindAddress] = true
	}
	for _, want := range []string{"127.0.1.2", "127.0.1.3", "127.0.1.4"} {
		if !addresses[want] {
			t.Errorf("bind address %s missing", want)
		}
	}
}

func TestParseNetstatMultipleBindsSamePort(t *testing.T) {
	output := `  Proto  Local Address          Foreign Address        State           PID
  TCP    10.9.1.1:8181          0.0.0.0:0              LISTENING       1101
  TCP    10.9.1.2:8181          0.0.0.0:0              LISTENING       1102
  TCP    10.9.1.3:8181          0.0.0.0:0              LISTENING       1103
`
	results := parseNetstat(output)
	if len(results) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(results))
	}
	addresses := map[string]bool{}
	for _, result := range results {
		addresses[result.BindAddress] = true
	}
	for _, want := range []string{"10.9.1.1", "10.9.1.2", "10.9.1.3"} {
		if !addresses[want] {
			t.Errorf("bind address %s missing", want)
		}
	}
}

func TestPortKey(t *testing.T) {
	row := ListeningPort{Port: 8080, BindAddress: "127.0.0.1"}
	if got := row.PortKey(); got != "8080:127.0.0.1" {
		t.Errorf("PortKey() = %q", got)
	}
}

func TestURLUsesBindAddress(t *testing.T) {
	cases := []struct {
		bind string
		want string
	}{
		{"0.0.0.0", "http://localhost:3100"},
		{"", "http://localhost:3100"},
		{"[::]", "http://localhost:3100"},
		{"::", "http://localhost:3100"},
		{"::1", "http://[::1]:3100"},
		{"10.1.2.5", "http://10.1.2.5:3100"},
		{"127.0.0.1", "http://127.0.0.1:3100"},
	}
	for _, tc := range cases {
		row := ListeningPort{Port: 3100, BindAddress: tc.bind}
		if got := row.URL(); got != tc.want {
			t.Errorf("URL() with bind %q = %q, want %q", tc.bind, got, tc.want)
		}
	}
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}

// bind_address and ip_version always agree. A dual-stack listener used to
// report "0.0.0.0" with "IPv6".
func TestNormalizeBind(t *testing.T) {
	cases := []struct {
		label           string
		input           string
		ipv6            bool
		expectedBind    string
		expectedVersion string
	}{
		// lsof spellings.
		{"lsof v4 wildcard", "*", false, "0.0.0.0", "IPv4"},
		{"lsof v6 wildcard", "*", true, "::", "IPv6"},
		{"netstat v6 wildcard", "[::]", true, "::", "IPv6"},
		{"ss v6 wildcard", "[::]", false, "::", "IPv6"},
		// Bare literals.
		{"bare v6 wildcard", "::", false, "::", "IPv6"},
		{"v4 wildcard spelled out", "0.0.0.0", false, "0.0.0.0", "IPv4"},
		{"v4 loopback", "127.0.0.1", false, "127.0.0.1", "IPv4"},
		{"v6 loopback", "[::1]", false, "::1", "IPv6"},
		// Literal addresses.
		{"v6 literal", "[fe80::2]", true, "fe80::2", "IPv6"},
		{"lan address", "10.1.2.5", false, "10.1.2.5", "IPv4"},
		// Empty means the v4 wildcard.
		{"empty is the v4 wildcard", "", false, "0.0.0.0", "IPv4"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			bind, version := normalizeBind(tc.input, tc.ipv6)
			if bind != tc.expectedBind || version != tc.expectedVersion {
				t.Errorf("normalizeBind(%q, %v) = (%q, %q), want (%q, %q)",
					tc.input, tc.ipv6, bind, version, tc.expectedBind, tc.expectedVersion)
			}
			if gotV6 := strings.Contains(bind, ":"); gotV6 != (version == "IPv6") {
				t.Errorf("bind %q and ip_version %q disagree", bind, version)
			}
		})
	}
}

// A process listening on both families yields one self-consistent row per
// family.
func TestParseLsofDualStackWildcard(t *testing.T) {
	output := `COMMAND   PID  USER   FD   TYPE   DEVICE SIZE/OFF NODE NAME
app     4321 dev     6u  IPv4  4234567      0t0  TCP *:4100 (LISTEN)
app     4321 dev     7u  IPv6  4234568      0t0  TCP *:4100 (LISTEN)
app     4321 dev     8u  IPv6  4234569      0t0  TCP [::1]:4200 (LISTEN)
`
	rows := parseLsof(output)
	if len(rows) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(rows), rows)
	}
	expected := map[string]string{
		"0.0.0.0": "IPv4",
		"::":      "IPv6",
		"::1":     "IPv6",
	}
	for _, result := range rows {
		version, known := expected[result.BindAddress]
		if !known {
			t.Errorf("unexpected bind address %q", result.BindAddress)
			continue
		}
		if result.IPVersion != version {
			t.Errorf("bind %q reported %q, want %q", result.BindAddress, result.IPVersion, version)
		}
	}
}

func TestParseNetstatDualStackWildcard(t *testing.T) {
	output := `  Proto  Local Address          Foreign Address        State           PID
  TCP    0.0.0.0:8200           0.0.0.0:0              LISTENING       1101
  TCP    [::]:8200              [::]:0                 LISTENING       1101
`
	results := parseNetstat(output)
	if len(results) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(results), results)
	}
	byBind := map[string]string{}
	for _, result := range results {
		byBind[result.BindAddress] = result.IPVersion
	}
	if byBind["0.0.0.0"] != "IPv4" {
		t.Errorf("0.0.0.0 reported %q", byBind["0.0.0.0"])
	}
	if byBind["::"] != "IPv6" {
		t.Errorf(":: reported %q", byBind["::"])
	}
}

// A verbatim `netstat -ano` shape: headers, IPv4 and IPv6 listeners,
// established connections that must be ignored, a pid-0 listener and a dev
// server. Windows sees no ports at all when this parser is wrong, so it is
// pinned against real output rather than a hand-shaped sample.
func TestParseNetstatRealCapture(t *testing.T) {
	output := "\r\nActive Connections\r\n\r\n" +
		"  Proto  Local Address          Foreign Address        State           PID\r\n" +
		"  TCP    0.0.0.0:136            0.0.0.0:0              LISTENING       1074\r\n" +
		"  TCP    0.0.0.0:446            0.0.0.0:0              LISTENING       5\r\n" +
		"  TCP    0.0.0.0:5051           0.0.0.0:0              LISTENING       0\r\n" +
		"  TCP    127.0.0.1:49770        127.0.0.1:49771        ESTABLISHED     7376\r\n" +
		"  TCP    127.0.0.1:53341        0.0.0.0:0              LISTENING       7376\r\n" +
		"  TCP    10.2.0.4:49775         20.209.14.65:443       ESTABLISHED     3912\r\n" +
		"  TCP    [::]:136               [::]:0                 LISTENING       1074\r\n" +
		"  TCP    [::1]:53341            [::]:0                 LISTENING       7376\r\n" +
		"  TCP    [::]:446               [::]:0                 TIME_WAIT       0\r\n" +
		"  UDP    0.0.0.0:5353           *:*                                    2748\r\n"

	parsed := parseNetstat(output)
	if len(parsed) != 6 {
		t.Fatalf("parsed %d rows, want the 6 LISTENING ones: %+v", len(parsed), parsed)
	}

	type fact struct {
		pid     int
		bind    string
		version string
	}
	byKey := map[string]fact{}
	for _, result := range parsed {
		byKey[result.PortKey()] = fact{result.PID, result.BindAddress, result.IPVersion}
	}
	want := map[string]fact{
		"136:0.0.0.0":     {1074, "0.0.0.0", "IPv4"},
		"446:0.0.0.0":     {5, "0.0.0.0", "IPv4"},
		"5051:0.0.0.0":    {0, "0.0.0.0", "IPv4"},
		"53341:127.0.0.1": {7376, "127.0.0.1", "IPv4"},
		"136:::":          {1074, "::", "IPv6"},
		"53341:::1":       {7376, "::1", "IPv6"},
	}
	for key, expected := range want {
		actual, found := byKey[key]
		if !found {
			t.Errorf("%s missing from the parse: %+v", key, byKey)
			continue
		}
		if actual != expected {
			t.Errorf("%s = %+v, want %+v", key, actual, expected)
		}
	}
	for _, unwanted := range []string{"49770:127.0.0.1", "49775:10.2.0.4", "446:::", "5353:0.0.0.0"} {
		if _, found := byKey[unwanted]; found {
			t.Errorf("%s was parsed; only TCP LISTENING rows belong in a scan", unwanted)
		}
	}
}
