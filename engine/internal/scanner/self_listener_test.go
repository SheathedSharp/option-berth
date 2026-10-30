package scanner

import (
	"os"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// The daemon's self-probe binds a loopback socket. A scan that overlaps the
// probe must not publish that socket as the daemon's own listener: it is not a
// service, and attributing it to the daemon's working directory would make it
// look like an undeclared listener of whatever checkout the daemon was started
// in.
func TestScanDoesNotPublishThisProcessOwnListener(t *testing.T) {
	self := ports.ListeningPort{
		Port: 49123, PID: os.Getpid(), Process: "oberth", Cwd: "/work/demo",
	}
	other := ports.ListeningPort{Port: 8123, PID: os.Getpid() + 1, Process: "python3"}
	l := loopWith(nil, self, other)

	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Ports) != 1 || snap.Ports[0].PID != other.PID {
		t.Fatalf("ports = %+v, want only the listener owned by pid %d", snap.Ports, other.PID)
	}
}
