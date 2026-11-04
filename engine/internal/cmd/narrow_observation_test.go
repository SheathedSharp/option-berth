package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// An observable verdict is only a clean document while the table covers every
// owner; a narrow table has to stay on the wire so `machine` absence is not
// read as "not listening".
func TestPublicObservationKeepsANarrowTable(t *testing.T) {
	narrow := publicObservation(&rpc.ObservationStatus{
		State: state.ObservationObservable, Source: "lsof",
	})
	if narrow == nil || narrow.Source != "lsof" {
		t.Fatalf("narrow observation = %+v, want it published", narrow)
	}
	if wide := publicObservation(&rpc.ObservationStatus{State: state.ObservationObservable}); wide != nil {
		t.Fatalf("complete observation = %+v, want it stripped", wide)
	}
}

func TestScanGroupsObservedPublishesNarrowCoverage(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, groups.ConfigName), []byte(`name: demo
services: []
machine:
  - name: redis
    port: 6379
`), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)

	previous := scanPorts
	scanPorts = func() ([]ports.ListeningPort, string, error) { return nil, "lsof", nil }
	t.Cleanup(func() { scanPorts = previous })

	_, gg, obs, err := scanGroupsObserved(context.Background())
	if err != nil {
		t.Fatalf("scanGroupsObserved: %v", err)
	}
	if obs == nil || obs.State != state.ObservationObservable || obs.Source != "lsof" {
		t.Fatalf("observation = %+v, want observable through lsof", obs)
	}
	var group *state.Group
	for i := range gg {
		if gg[i].Name == "demo" {
			group = &gg[i]
			break
		}
	}
	if group == nil || len(group.Machine) != 1 {
		t.Fatalf("groups = %+v, want the demo manifest with one machine reference", gg)
	}
	if group.Machine[0].Listening || group.Machine[0].ListeningKnown {
		t.Fatalf("machine = %+v, want an absent port to be unknown under a narrow table", group.Machine[0])
	}
}

func TestStatusPrintsUnknownForANarrowMachineReference(t *testing.T) {
	restore := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = restore })

	doc := statusDocument{}
	doc.Worktree.Machine = []state.MachineRef{
		{Name: "db", Port: 5432, ListeningKnown: false},
		{Name: "cache", Port: 6379, ListeningKnown: true},
	}
	out := captureStdout(t, func() { printStatusMachine(doc) })
	if !strings.Contains(out, "db 5432 listening unknown") {
		t.Errorf("status = %q, want the unknown dependency named as unknown", out)
	}
	if !strings.Contains(out, "cache 6379 not listening") {
		t.Errorf("status = %q, want a known absence to stay negative", out)
	}
}
