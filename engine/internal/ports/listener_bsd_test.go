package ports

import (
	"os"
	"testing"
)

const darwinTCPHeader = "Proto Recv-Q Send-Q Local Address Foreign Address (state) rxbytes txbytes rhiwat shiwat process:pid state options\n"

func TestDarwinTCPTableUsesNamedPIDColumn(t *testing.T) {
	text := darwinTCPHeader +
		"tcp4 0 0 127.0.0.1.8123 *.* LISTEN 0 0 131072 131072 node:42 0 0\n" +
		"tcp6 0 0 ::1.8124 *.* LISTEN 0 0 131072 131072 My App:43 0 0\n" +
		"tcp4 0 0 *.8125 *.* LISTEN 0 0 131072 131072 other:0 0 0\n" +
		"tcp4 0 0 127.0.0.1.8123 *.* LISTEN 0 0 131072 131072 node:42 0 0\n"
	rows, err := decodeDarwinNetstat(text)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%+v error=%v", rows, err)
	}
	if rows[0].PID != 42 || rows[0].Port != 8123 || rows[0].BindAddress != "127.0.0.1" {
		t.Fatalf("IPv4=%+v", rows[0])
	}
	if rows[1].PID != 43 || rows[1].Process != "My App" || rows[1].BindAddress != "::1" {
		t.Fatalf("IPv6=%+v", rows[1])
	}
	if rows[2].PID != 0 || rows[2].BindAddress != "0.0.0.0" {
		t.Fatalf("unknown owner=%+v", rows[2])
	}
}

// tcp46 is how macOS prints a dual-stack socket. Go's net.Listen("tcp", ":p"),
// Node's listen(p) and Java's default bind all land here, so dropping the row
// made those services invisible to status, readiness and dependency waits.
func TestDarwinTCPTableAcceptsDualStackListeners(t *testing.T) {
	text := darwinTCPHeader +
		"tcp46 0 0 *.8123 *.* LISTEN 0 0 131072 131072 node:42 0 0\n" +
		"tcp46 0 0 *.8123 *.* LISTEN 0 0 131072 131072 node:42 0 0\n"
	rows, err := decodeDarwinNetstat(text)
	if err != nil {
		t.Fatalf("dual-stack row rejected: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%+v, want both address families once", rows)
	}
	byBind := map[string]ListeningPort{}
	for _, row := range rows {
		byBind[row.BindAddress] = row
	}
	v4, ok := byBind["0.0.0.0"]
	if !ok || v4.IPVersion != "IPv4" || v4.PID != 42 || v4.Port != 8123 {
		t.Fatalf("IPv4 half = %+v (present=%v)", v4, ok)
	}
	v6, ok := byBind["::"]
	if !ok || v6.IPVersion != "IPv6" || v6.PID != 42 || v6.Port != 8123 {
		t.Fatalf("IPv6 half = %+v (present=%v)", v6, ok)
	}

	merged := mergeAddresses(rows)
	if len(merged) != 1 {
		t.Fatalf("merged=%+v, want one listener for one socket", merged)
	}
	if merged[0].BindAddress != "0.0.0.0" || len(merged[0].BindAddresses) != 1 || merged[0].BindAddresses[0] != "::" {
		t.Fatalf("merged addresses = %+v", merged[0])
	}
}

func TestDarwinTCPTableDualStackLiteralAddressStaysOneRow(t *testing.T) {
	rows, err := decodeDarwinNetstat(darwinTCPHeader +
		"tcp46 0 0 2001:db8::1.8123 *.* LISTEN 0 0 131072 131072 node:42 0 0\n")
	if err != nil || len(rows) != 1 || rows[0].BindAddress != "2001:db8::1" || rows[0].IPVersion != "IPv6" {
		t.Fatalf("rows=%+v error=%v", rows, err)
	}
}

// The fixture keeps the real column layout (including process:pid and the
// traffic columns) for the three default-bind runtimes users hit on macOS.
func TestDarwinTCPTableFixtureAcceptsDualStackServices(t *testing.T) {
	data, err := os.ReadFile("testdata/darwin_netstat_tcp46.txt")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := decodeDarwinNetstat(string(data))
	if err != nil {
		t.Fatalf("fixture rejected: %v", err)
	}
	merged := mergeAddresses(rows)
	if len(merged) != 3 {
		t.Fatalf("merged=%+v, want one row per dual-stack socket", merged)
	}
	want := map[int]struct {
		pid  int
		name string
	}{
		18123: {4242, "go-app"},
		18124: {4243, "node"},
		18125: {4244, "java"},
	}
	for _, row := range merged {
		service, ok := want[row.Port]
		if !ok {
			t.Fatalf("unexpected port %d: %+v", row.Port, row)
		}
		if row.PID != service.pid || row.Process != service.name {
			t.Errorf("port %d = pid %d %q, want pid %d %q", row.Port, row.PID, row.Process, service.pid, service.name)
		}
		if row.BindAddress != "0.0.0.0" {
			t.Errorf("port %d primary bind = %q, want 0.0.0.0", row.Port, row.BindAddress)
		}
		if len(row.BindAddresses) != 1 || row.BindAddresses[0] != "::" {
			t.Errorf("port %d secondary binds = %v, want [::]", row.Port, row.BindAddresses)
		}
	}
}

func TestDarwinTCPTableAcceptsLegacyHeaderAndEmptyTable(t *testing.T) {
	header := "Proto Recv-Q Send-Q Local Address Foreign Address (state) rhiwat shiwat pid epid\n"
	rows, err := decodeDarwinNetstat(header + "tcp6 0 0 *.8123 *.* LISTEN 128 128 44 0\n")
	if err != nil || len(rows) != 1 || rows[0].PID != 44 || rows[0].BindAddress != "::" {
		t.Fatalf("rows=%+v error=%v", rows, err)
	}
	if rows, err := decodeDarwinNetstat(header); err != nil || len(rows) != 0 {
		t.Fatalf("empty=%+v %v", rows, err)
	}
}

func TestDarwinTCPTableDoesNotTurnMalformedOutputIntoEmptySuccess(t *testing.T) {
	for _, text := range []string{"", "permission denied\n", darwinTCPHeader + "tcp4 0\n",
		darwinTCPHeader + "tcp4 0 0 127.0.0.1.8123 *.* LISTEN\n",
		darwinTCPHeader + "tcp4 0 0 127.0.0.1.bad *.* LISTEN 0 0 128 128 node:42\n",
		darwinTCPHeader + "tcp4 0 0 127.0.0.1.8123 *.* LISTEN 0 0 128 128 node:bad\n"} {
		if rows, err := decodeDarwinNetstat(text); err == nil || len(rows) != 0 {
			t.Fatalf("accepted malformed input: %+v %v", rows, err)
		}
	}
}

func TestDarwinTCPTablePreservesFullIPv6Address(t *testing.T) {
	const full = "2001:db8:1234:5678:abcd:ef01:2345:6789"
	rows, err := decodeDarwinNetstat(darwinTCPHeader + "tcp6 0 0 " + full + ".8123 *.* LISTEN 0 0 131072 131072 fixture:42 0 0\n")
	if err != nil || len(rows) != 1 || rows[0].BindAddress != full || rows[0].PID != 42 {
		t.Fatalf("full IPv6 identity lost: rows=%+v err=%v", rows, err)
	}
	// This is the truncation Darwin emits without -l. Keep rejecting it;
	// do not repair unknown bytes or silently discard the observation.
	if _, err := decodeDarwinNetstat(darwinTCPHeader + "tcp6 0 0 " + full[:16] + ".8123 *.* LISTEN 0 0 131072 131072 fixture:42 0 0\n"); err == nil {
		t.Fatal("truncated IPv6 address was accepted")
	}
}
