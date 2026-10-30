package scanner

import (
	"os"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// The daemon's self-probe binds a loopback socket in the daemon process. The
// production scan must not publish that socket as the daemon's own listener: it
// is not a service, and attributing it to the daemon's working directory would
// make it look like an undeclared listener of whatever checkout the daemon was
// started in.
func TestWithoutOwnListenersDropsThisProcess(t *testing.T) {
	self := ports.ListeningPort{Port: 49123, PID: os.Getpid(), Process: "oberth"}
	other := ports.ListeningPort{Port: 8123, PID: os.Getpid() + 1, Process: "python3"}

	got := withoutOwnListeners([]ports.ListeningPort{self, other})
	if len(got) != 1 || got[0].PID != other.PID || got[0].Port != other.Port {
		t.Fatalf("rows = %+v, want only the listener owned by pid %d", got, other.PID)
	}
}
