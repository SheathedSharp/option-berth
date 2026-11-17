package ports

import (
	"reflect"
	"testing"
)

// linuxNetstatTable is `netstat -tlnp` as a Linux host prints it, in every shape
// the decoder has to separate: a loopback row whose owner this user may not
// inspect, a resolved name, an IPv6 wildcard, an IPv6 loopback, an IPv4
// wildcard, a connected TCP row that is not a listener, an unreadable port, and
// a UDP row that -t should never have produced. Identities are synthetic.
const linuxNetstatTable = `Active Internet connections (only servers)
Proto Recv-Q Send-Q Local Address           Foreign Address         State       PID/Program name
tcp        0      0 127.0.0.1:18120         0.0.0.0:*               LISTEN      -
tcp        0      0 127.0.0.53:18121        0.0.0.0:*               LISTEN      742/named
tcp6       0      0 :::18122                :::*                    LISTEN      4242/node
tcp6       0      0 ::1:18123               :::*                    LISTEN      4243/postgres
tcp        0      0 0.0.0.0:18124           0.0.0.0:*               LISTEN      4244/java
tcp        0      0 127.0.0.1:18125          127.0.0.1:53344         ESTABLISHED 4245/curl
tcp        0      0 127.0.0.1:bad           0.0.0.0:*               LISTEN      4246/ghost
udp        0      0 0.0.0.0:18126           0.0.0.0:*                           742/named
`

func TestLinuxNetstatDecodesListenerRowsAndTheirOwners(t *testing.T) {
	got := decodeListeners(linuxNetstatTable, linuxNetstatListeners)
	want := []ListeningPort{
		{Port: 18120, PID: 0, Process: "", BindAddress: "127.0.0.1", IPVersion: "IPv4"},
		{Port: 18121, PID: 742, Process: "named", BindAddress: "127.0.0.53", IPVersion: "IPv4"},
		{Port: 18122, PID: 4242, Process: "node", BindAddress: "::", IPVersion: "IPv6"},
		{Port: 18123, PID: 4243, Process: "postgres", BindAddress: "::1", IPVersion: "IPv6"},
		{Port: 18124, PID: 4244, Process: "java", BindAddress: "0.0.0.0", IPVersion: "IPv4"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %+v\nwant   %+v", got, want)
	}
}

// Without -p netstat prints no owner column at all. The port is still a fact;
// the process is unknown, and guessing one would attribute a listener to a
// process that never held it.
func TestLinuxNetstatWithoutOwnerColumnKeepsThePortAndDropsTheOwner(t *testing.T) {
	got := decodeListeners("tcp 0 0 127.0.0.1:18120 0.0.0.0:* LISTEN\n", linuxNetstatListeners)
	if len(got) != 1 || got[0].Port != 18120 || got[0].PID != 0 || got[0].Process != "" {
		t.Fatalf("rows = %+v, want the port without an owner", got)
	}
}

// A header with no row under it is a genuinely idle machine, not a failed read:
// the chain only falls through when a collector printed no table at all.
func TestLinuxNetstatHeaderOnlyIsAnEmptyTable(t *testing.T) {
	text := "Proto Recv-Q Send-Q Local Address           Foreign Address         State       PID/Program name\n"
	if got := decodeListeners(text, linuxNetstatListeners); len(got) != 0 {
		t.Fatalf("rows = %+v, want none", got)
	}
}
