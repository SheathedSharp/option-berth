package ports

import (
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func TestListenerDecodersRecoverAfterLongDiagnosticsAndWithoutHeaders(t *testing.T) {
	rows := []struct {
		format listenerFormat
		row    string
	}{
		{lsofListeners, "node 42 user 6u IPv4 1 0t0 TCP 127.0.0.1:8080 (LISTEN)"},
		{ssListeners, `LISTEN 0 128 127.0.0.1:8080 0.0.0.0:* users:(("node",pid=42,fd=6))`},
		{netstatListeners, "TCP 127.0.0.1:8080 0.0.0.0:0 LISTENING 42"},
	}
	for _, tt := range rows {
		t.Run(fmt.Sprint(tt.format), func(t *testing.T) {
			for _, prefix := range []string{"", "\n\r\n", strings.Repeat("diagnostic ", 9000) + "\n"} {
				got := decodeListeners(prefix+tt.row+"\r\n"+tt.row, tt.format)
				if len(got) != 1 || got[0].PID != 42 || got[0].Port != 8080 {
					t.Fatalf("format=%d prefix length=%d result=%+v", tt.format, len(prefix), got)
				}
			}
		})
	}
}

func TestListenerDecodersRejectInvalidFacts(t *testing.T) {
	endpoints := []string{"127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:-1", "127.0.0.1:1x", "[::1:8080", "::1]:8080", "[]:8080", ":8080", "not-an-address:8080", "127.0.0.1:3000->127.0.0.1:4000"}
	for _, endpoint := range endpoints {
		inputs := []string{
			fmt.Sprintf("node 42 user 6u IPv4 1 0t0 TCP %s (LISTEN)", endpoint),
			fmt.Sprintf("LISTEN 0 128 %s *:*", endpoint),
			fmt.Sprintf("TCP %s 0.0.0.0:0 LISTENING 42", endpoint),
		}
		for format, input := range inputs {
			if got := decodeListeners(input, listenerFormat(format)); len(got) != 0 {
				t.Fatalf("accepted invalid %q: %+v", endpoint, got)
			}
		}
	}
	for _, input := range []struct {
		format listenerFormat
		row    string
	}{
		{lsofListeners, "node -1 user 6u IPv4 1 0t0 TCP *:8080 (LISTEN)"},
		{lsofListeners, "node 42 user 6u IPv4 1 0t0 TCP *:8080 (ESTABLISHED)"},
		{lsofListeners, "node 42 user 6u IPv4 1 0t0 UDP *:8080"},
		{ssListeners, "ESTAB 0 128 127.0.0.1:8080 *:*"},
		{ssListeners, "udp UNCONN 0 128 127.0.0.1:8080 *:*"},
		{netstatListeners, "TCP 0.0.0.0:8080 0.0.0.0:0 LISTENING -1"},
	} {
		if got := decodeListeners(input.row, input.format); len(got) != 0 {
			t.Fatalf("accepted non-listener %q", input.row)
		}
	}
}

func TestSSDecodesQuotedNamesAndOptionalNetid(t *testing.T) {
	row := `tcp6 LISTEN 0 128 [fe80::1%lo]:8080 [::]:* users:(("worker \"pid=7\"",pid=42,fd=6))`
	got := parseSS(row)
	if len(got) != 1 || got[0].PID != 42 || got[0].Process != `worker "pid=7"` || got[0].BindAddress != "fe80::1%lo" || got[0].IPVersion != "IPv6" {
		t.Fatalf("quoted/zone facts = %+v", got)
	}
	for _, text := range []string{`users:(("unterminated`, `users:(("worker",pid=-1,fd=6))`, `users:(("worker",pid=12junk,fd=6))`} {
		if pid, _ := parseSSUsers(text); pid != 0 {
			t.Fatalf("invented pid=%d from %q", pid, text)
		}
	}
}

func TestMergeAddressesOwnsItsOutputAndIsIdempotent(t *testing.T) {
	secondary := make([]string, 1, 4)
	secondary[0] = "::1"
	rows := []ListeningPort{
		{PID: 42, Port: 8080, BindAddress: "127.0.0.1", BindAddresses: secondary},
		{PID: 42, Port: 8080, BindAddress: "::1", BindAddresses: []string{"0.0.0.0"}},
	}
	got := mergeAddresses(rows)
	if len(got) != 1 || !reflect.DeepEqual(got[0].BindAddresses, []string{"::1", "0.0.0.0"}) {
		t.Fatalf("merged=%+v", got)
	}
	if !reflect.DeepEqual(got, mergeAddresses(got)) {
		t.Fatal("merging again changed the result")
	}
	got[0].BindAddresses[0] = "mutated"
	if secondary[0] != "::1" || secondary[:cap(secondary)][1] != "" {
		t.Fatal("merge mutated the input backing array")
	}
}

func FuzzListenerDecoder(f *testing.F) {
	f.Add(uint8(lsofListeners), "node 42 user 6u IPv4 1 0t0 TCP *:8080 (LISTEN)")
	f.Add(uint8(ssListeners), `LISTEN 0 128 *:8080 *:* users:(("worker",pid=42,fd=6))`)
	f.Add(uint8(netstatListeners), "TCP [::]:8080 [::]:0 LISTENING 0")
	f.Fuzz(func(t *testing.T, format uint8, text string) {
		seen := map[listenerKey]bool{}
		for _, row := range decodeListeners(text, listenerFormat(format)) {
			if row.Port <= 0 || row.Port > 65535 || row.PID < 0 {
				t.Fatalf("invalid fact: %+v", row)
			}
			if _, err := netip.ParseAddr(row.BindAddress); err != nil {
				t.Fatal(err)
			}
			if (row.IPVersion == "IPv6") != strings.Contains(row.BindAddress, ":") {
				t.Fatalf("family mismatch: %+v", row)
			}
			key := listenerKey{row.Port, row.BindAddress}
			if seen[key] {
				t.Fatalf("duplicate: %+v", key)
			}
			seen[key] = true
		}
	})
}
