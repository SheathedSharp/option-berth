package groups

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/sheathedsharp/option-berth/engine/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// exitRegistry knows how one service ended and nothing else.
type exitRegistry struct{ NoRuns }

func (exitRegistry) ServiceFacts() servicefacts.Snapshot {
	return servicefacts.Capture(nil, []servicefacts.ExitRecord{{
		Key:  servicefacts.Key{Group: "shop", Service: "api"},
		Exit: state.ServiceExit{Code: 1, Reason: "crashed", At: "2026-09-13T10:00:00Z", RunID: "a"},
	}})
}

func strPtr(s string) *string { return &s }

func declaredService(s Service) state.Service {
	row := ServiceRow(s)
	path := filepath.Join(paths.Logs(), "shop", s.Name+".log")
	row.LogPath = &path
	return row
}

// shopConfig is the manifest these tests publish: the services they care about,
// in a project called "shop" that exists on no disk.
func shopConfig(services ...Service) *Config {
	return &Config{
		Name: "shop", Dir: "/nowhere/shop", Path: "/nowhere/shop/" + ConfigName,
		Services: services,
	}
}

func shopGroup(status string, running int, services ...state.Service) []state.Group {
	root := "/nowhere/shop"
	cfgPath := "/nowhere/shop/" + ConfigName
	return []state.Group{{
		Name: "shop", Repo: "shop", Identity: WorktreeToken(root),
		Source: state.SourceFile, RootDir: &root, ConfigPath: &cfgPath,
		Status: status, Members: []int{}, Services: services,
		Machine: []state.MachineRef{}, RunningServices: running,
	}}
}

func requireGroups(t *testing.T, got, want []state.Group) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("groups = %#v, want %#v", got, want)
	}
}

// TestGroupsCarryTheLastExitOfAServiceThatIsDown: a stopped service reports how
// it ended, so a client can show "crashed" rather than only "not running".
func TestGroupsCarryTheLastExitOfAServiceThatIsDown(t *testing.T) {
	idx := NewIndex()
	idx.Add(shopConfig(Service{Name: "api", Cmd: "run-api"}, Service{Name: "web", Cmd: "run-web"}))

	built := GroupsWith(nil, idx, exitRegistry{}, false)
	api := declaredService(Service{Name: "api", Cmd: "run-api"})
	api.LastExit = &state.ServiceExit{Code: 1, Reason: "crashed", At: "2026-09-13T10:00:00Z", RunID: "a"}
	requireGroups(t, built, shopGroup("stopped", 0, api, declaredService(Service{Name: "web", Cmd: "run-web"})))
}

// TestGroupsWithoutARegistryHaveNoExits: the direct-scan path has no registry at
// all, and must still build its groups.
func TestGroupsWithoutARegistryHaveNoExits(t *testing.T) {
	idx := NewIndex()
	idx.Add(shopConfig(Service{Name: "api", Cmd: "run-api"}))
	built := Groups(nil, idx)
	requireGroups(t, built, shopGroup("stopped", 0, declaredService(Service{Name: "api", Cmd: "run-api"})))
}

// liveRegistry knows which services option-berth has a live run of.
type liveRegistry struct{ NoRuns }

func (liveRegistry) ServiceFacts() servicefacts.Snapshot {
	return servicefacts.Capture([]servicefacts.RunRecord{{
		Key: servicefacts.Key{Group: "shop", Service: "worker"},
		Run: state.ServiceRun{ID: "run-worker", PID: 4242, StartedAt: "2026-09-13T10:00:00Z"},
	}}, nil)
}

// TestGroupsSeeAServiceThatHoldsNoPort: the join above matches services to
// ports, so a `worker` — declared, running, listening on nothing — used to come
// back as "not running" however plainly the run registry knew it. "谁在跑" is
// what a reader asks this field, and for those services the registry is the only
// thing that can answer.
//
// The port stays absent: it is alive and listening on nothing, and inventing a
// port for it would be a different lie.
func TestGroupsSeeAServiceThatHoldsNoPort(t *testing.T) {
	index := NewIndex()
	index.Add(shopConfig(Service{Name: "worker", Cmd: "run-worker"}, Service{Name: "api", Cmd: "run-api"}))

	gg := GroupsWith(nil, index, liveRegistry{}, false)
	worker := declaredService(Service{Name: "worker", Cmd: "run-worker"})
	worker.Running = true
	worker.PID = intPtr(4242)
	worker.RunID = strPtr("run-worker")
	worker.StartedAt = strPtr("2026-09-13T10:00:00Z")
	requireGroups(t, gg, shopGroup("partial", 1, worker, declaredService(Service{Name: "api", Cmd: "run-api"})))
}

type driftRegistry struct{ NoRuns }

func (driftRegistry) ServiceFacts() servicefacts.Snapshot {
	return servicefacts.Capture([]servicefacts.RunRecord{{
		Key: servicefacts.Key{Group: "shop", Service: "api"},
		Run: state.ServiceRun{ID: "run-api", PID: 7, Cmd: "new-api", Cwd: "/nowhere/shop"},
	}}, nil)
}

func TestGroupsExposeManifestRuntimeMismatch(t *testing.T) {
	index := NewIndex()
	index.Add(shopConfig(Service{Name: "api", Cmd: "old-api"}))
	gg := GroupsWith(nil, index, driftRegistry{}, false)
	api := declaredService(Service{Name: "api", Cmd: "old-api"})
	api.Running = true
	api.PID = intPtr(7)
	api.RunID = strPtr("run-api")
	api.RuntimeCmd = strPtr("new-api")
	api.RuntimeCwd = strPtr("/nowhere/shop")
	api.ManifestRuntimeMismatch = true
	requireGroups(t, gg, shopGroup("running", 1, api))
}
