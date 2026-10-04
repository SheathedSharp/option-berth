package ports

import "testing"

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
