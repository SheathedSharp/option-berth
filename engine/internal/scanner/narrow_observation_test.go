package scanner

import (
	"context"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// A narrow scan is still a usable table for what it shows, but the snapshot
// has to publish the collector: otherwise an absent `machine:` port reads as
// "not listening" when the collector simply cannot see that owner.
func TestNarrowScanPublishesItsCollectorAndUnknownMachinePorts(t *testing.T) {
	st := openStore(t)
	dir := t.TempDir()
	writeConfig(t, dir, `name: shop
services: []
machine:
  - name: redis
    port: 6379
`)
	l := New(Options{
		DaemonVersion: "test",
		Store:         st,
		ScanSource: func(context.Context, Include) ([]ports.ListeningPort, string, error) {
			return []ports.ListeningPort{{Port: 8123, PID: 42, Process: "python3", Cwd: dir}}, "lsof", nil
		},
	})

	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Observation == nil ||
		snap.Observation.State != state.ObservationObservable ||
		snap.Observation.Source != "lsof" {
		t.Fatalf("observation = %+v, want observable through lsof", snap.Observation)
	}
	var group *state.Group
	for i := range snap.Groups {
		if snap.Groups[i].Name == "shop" {
			group = &snap.Groups[i]
			break
		}
	}
	if group == nil || len(group.Machine) != 1 {
		t.Fatalf("groups = %+v, want the shop manifest with one machine reference", snap.Groups)
	}
	if group.Machine[0].Listening || group.Machine[0].ListeningKnown {
		t.Fatalf("machine = %+v, want an absent port to be unknown under lsof", group.Machine[0])
	}
	if got := l.lastNarrowForTest(); !got {
		t.Fatal("the last scan did not carry its narrow coverage into a republish")
	}
}

// lastNarrowForTest reads the field a republish reuses.
func (l *Loop) lastNarrowForTest() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastNarrow
}
