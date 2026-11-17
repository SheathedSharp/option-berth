package cmd

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/config"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// mergedDirectScan points the direct read at a merge for the duration of one
// test, and fails the test if the single-collector seam answers instead.
func mergedDirectScan(t *testing.T, rows []ports.ListeningPort, sources []string) {
	t.Helper()
	previousConfig, previousPorts, previousSources := loadedConfig, scanPorts, scanSources
	cfg := &config.Config{}
	cfg.RuntimeService.ObserveSources = 2
	loadedConfig = cfg
	scanPorts = func() ([]ports.ListeningPort, string, error) {
		t.Error("the single-collector seam answered a configured merge")
		return nil, "", nil
	}
	scanSources = func() ([]ports.ListeningPort, []string, error) { return rows, sources, nil }
	t.Cleanup(func() {
		loadedConfig, scanPorts, scanSources = previousConfig, previousPorts, previousSources
	})
}

// A configured merge publishes every contributor, and its absence stays
// unknown: a port missing from one of the merged tables could be sitting in
// another, so `machine:` must not read as "not listening".
func TestScanGroupsObservedPublishesAMergedTable(t *testing.T) {
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
	mergedDirectScan(t,
		[]ports.ListeningPort{{Port: 8123, PID: os.Getpid(), BindAddress: "127.0.0.1", Cwd: dir}},
		[]string{"ss", "netstat"})

	got, gg, obs, err := scanGroupsObserved(context.Background())
	if err != nil {
		t.Fatalf("scanGroupsObserved: %v", err)
	}
	if len(got) != 1 || got[0].Port != 8123 {
		t.Fatalf("rows = %+v, want the merged row", got)
	}
	if obs == nil || obs.State != state.ObservationObservable {
		t.Fatalf("observation = %+v, want an observable merged read", obs)
	}
	if want := []string{"ss", "netstat"}; !reflect.DeepEqual(obs.Sources, want) {
		t.Fatalf("sources = %v, want %v", obs.Sources, want)
	}
	if obs.Source != "" {
		t.Fatalf("source = %q, want no single source for a merged table", obs.Source)
	}
	if !strings.Contains(obs.Detail, "merged from ss, netstat") {
		t.Fatalf("detail = %q, want the contributors named", obs.Detail)
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
		t.Fatalf("machine = %+v, want an absent port to be unknown under a merged table", group.Machine[0])
	}
}

// The default configuration keeps the single-collector read, whose alternative
// travels in observation.source rather than in a list. Tests that replace only
// scanPorts depend on this, and so does every machine that never sets the key.
func TestDefaultConfigurationKeepsTheSingleCollectorRead(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	chdir(t, t.TempDir())
	previous := scanPorts
	scanPorts = func() ([]ports.ListeningPort, string, error) { return nil, "lsof", nil }
	t.Cleanup(func() { scanPorts = previous })

	if got := loadedConfig.RuntimeService.ResolvedObserveSources(); got != 1 {
		t.Fatalf("observe_sources = %d, want the default single collector", got)
	}
	_, source, sources, err := scanDirect()
	if err != nil {
		t.Fatalf("scanDirect: %v", err)
	}
	if source != "lsof" || sources != nil {
		t.Fatalf("source=%q sources=%v, want the single collector's name and no list", source, sources)
	}
}
