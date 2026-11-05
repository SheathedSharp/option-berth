package ports

import (
	"reflect"
	"testing"
)

// One lsof listing must answer for every port at once, with the same numbers
// the per-port calls produced: a loopback pair was seen at both ends and
// halved.
func TestCountLsofByPort(t *testing.T) {
	listing := `COMMAND   PID   USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
node     2000 dev     24u  IPv4 0x1111      0t0  TCP 127.0.0.1:4000->127.0.0.1:61001 (ESTABLISHED)
curl     2001 dev      5u  IPv4 0x1111      0t0  TCP 127.0.0.1:61001->127.0.0.1:4000 (ESTABLISHED)
node     2000 dev     25u  IPv4 0x2222      0t0  TCP 127.0.0.1:4000->127.0.0.1:61002 (ESTABLISHED)
curl     2002 dev      5u  IPv4 0x2222      0t0  TCP 127.0.0.1:61002->127.0.0.1:4000 (ESTABLISHED)
vite     2003 dev     30u  IPv6 0x3333      0t0  TCP [::1]:7001->[::1]:62001 (ESTABLISHED)
chrome   2004 dev     88u  IPv6 0x3333      0t0  TCP [::1]:62001->[::1]:7001 (ESTABLISHED)
sshd      900 root     3u  IPv4 0x4444      0t0  TCP 10.0.0.2:22->10.0.0.9:51000 (ESTABLISHED)
`
	counts := countLsofByPort(listing)
	cases := []struct {
		port int
		want int
	}{
		{4000, 2}, // two loopback clients
		{7001, 1}, // one, over IPv6
		{22, 0},   // remote peer: only one end is local, as before
	}
	for _, tc := range cases {
		if counts[tc.port] != tc.want {
			t.Errorf("port %d = %d connections, want %d (all: %v)",
				tc.port, counts[tc.port], tc.want, counts)
		}
	}
}

func TestCountSSByPortIgnoresTheStateColumn(t *testing.T) {
	// ss drops the State column when filtering by state and keeps it otherwise.
	// Both shapes must read the same.
	filtered := `Recv-Q Send-Q Local Address:Port  Peer Address:Port
0      0      127.0.0.1:4100      127.0.0.1:63001
0      0      127.0.0.1:4100      127.0.0.1:63002
0      0      127.0.0.1:63001     127.0.0.1:4100
0      0      [::1]:7101          [::1]:64001
`
	unfiltered := `State  Recv-Q Send-Q Local Address:Port  Peer Address:Port
ESTAB  0      0      127.0.0.1:4100      127.0.0.1:63001
ESTAB  0      0      127.0.0.1:4100      127.0.0.1:63002
ESTAB  0      0      127.0.0.1:63001     127.0.0.1:4100
ESTAB  0      0      [::1]:7101          [::1]:64001
`
	if !reflect.DeepEqual(countSSByPort(filtered), countSSByPort(unfiltered)) {
		t.Errorf("ss with and without its State column read differently:\n %v\n %v",
			countSSByPort(filtered), countSSByPort(unfiltered))
	}
	counts := countSSByPort(filtered)
	// ss reports only the local end, matching the old `sport = :<port>` filter:
	// nothing is halved.
	if counts[4100] != 2 || counts[7101] != 1 || counts[63001] != 1 {
		t.Errorf("counts = %v, want 4100:2 7101:1 63001:1", counts)
	}
}

func TestCountNetstatByPort(t *testing.T) {
	listing := `
Active Connections
  Proto  Local Address          Foreign Address        State           PID
  TCP    127.0.0.1:4200         127.0.0.1:65001        ESTABLISHED     3000
  TCP    127.0.0.1:4200         127.0.0.1:65002        ESTABLISHED     3000
  TCP    0.0.0.0:7201           0.0.0.0:0              LISTENING       3003
`
	counts := countNetstatByPort(listing)
	if counts[4200] != 2 {
		t.Errorf("port 4200 = %d, want 2 (all: %v)", counts[4200], counts)
	}
	if counts[7201] != 0 {
		t.Errorf("a LISTENING row was counted as a connection: %v", counts)
	}
}

// `ps -M` prints a process line then one line per thread, with the pid in a
// different column on each. The count is every line but the header, per pid,
// which is what the one-fork-per-pid version reported.
func TestCountPSThreads(t *testing.T) {
	listing := `USER     PID   TT   %CPU STAT PRI     STIME     UTIME COMMAND
root       1   ??    0.0 S    31T   0:00.40   0:00.09 /sbin/launchd
           1         0.0 S    37T   0:00.02   0:00.02
           1         0.1 S    37T   0:00.01   0:00.01
dev     2000   ??    3.1 S    31T   0:00.01   0:00.01 node server.js
        2000         0.0 S    37T   0:00.00   0:00.00
`
	counts := countPSThreads(listing, map[int]bool{1: true, 2000: true})
	want := map[int]int{1: 3, 2000: 2}
	if !reflect.DeepEqual(counts, want) {
		t.Errorf("thread counts = %v, want %v", counts, want)
	}
}

// A pid ps did not report is absent rather than zero, so a line belonging to
// another process cannot become its fact.
func TestCountPSThreadsIgnoresUnrequestedPIDs(t *testing.T) {
	listing := `USER     PID   TT   %CPU STAT PRI     STIME     UTIME COMMAND
dev     2000   ??    3.1 S    31T   0:00.01   0:00.01 node server.js
        2000         0.0 S    37T   0:00.00   0:00.00
`
	counts := countPSThreads(listing, map[int]bool{2000: true, 4000: true})
	if _, present := counts[4000]; present {
		t.Errorf("counts = %v, want no entry for a pid ps did not report", counts)
	}
	if counts[2000] != 2 {
		t.Errorf("pid 2000 = %d threads, want 2", counts[2000])
	}
}
