package groups

import (
	"testing"

	"github.com/sheathedsharp/option-berth/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// exitRegistry knows how one service ended and nothing else.
type exitRegistry struct{ NoRuns }

func (exitRegistry) ServiceFacts() servicefacts.Snapshot {
	return servicefacts.Capture(nil, []servicefacts.ExitRecord{{
		Key:  servicefacts.Key{Group: "shop", Service: "api"},
		Exit: state.ServiceExit{Code: 1, Reason: "crashed", At: "2026-09-13T10:00:00Z", RunID: "a"},
	}})
}

// TestGroupsCarryTheLastExitOfAServiceThatIsDown: a stopped service says how
// it ended, so a client can show "crashed" rather than only "not running".
func TestGroupsCarryTheLastExitOfAServiceThatIsDown(t *testing.T) {
	index := NewIndex()
	index.Add(&Config{
		Name: "shop", Dir: "/nowhere/shop", Path: "/nowhere/shop/" + ConfigName,
		Services: []Service{{Name: "api", Cmd: "run-api"}, {Name: "web", Cmd: "run-web"}},
	})

	gg := GroupsWith(nil, index, exitRegistry{})
	if len(gg) != 1 {
		t.Fatalf("groups = %+v, want the one config's group", gg)
	}
	byName := map[string]state.Service{}
	for _, s := range gg[0].Services {
		byName[s.Name] = s
	}
	api, web := byName["api"], byName["web"]
	if api.LastExit == nil || api.LastExit.Code != 1 || api.LastExit.Reason != "crashed" {
		t.Errorf("api.LastExit = %+v, want the crash", api.LastExit)
	}
	if web.LastExit != nil {
		t.Errorf("web.LastExit = %+v, want nothing for a service that never ran", web.LastExit)
	}
}

// TestGroupsWithoutARegistryHaveNoExits: the direct-scan path has no registry
// at all, and must still build its groups.
func TestGroupsWithoutARegistryHaveNoExits(t *testing.T) {
	index := NewIndex()
	index.Add(&Config{
		Name: "shop", Dir: "/nowhere/shop", Path: "/nowhere/shop/" + ConfigName,
		Services: []Service{{Name: "api", Cmd: "run-api"}},
	})
	gg := Groups(nil, index)
	if len(gg) != 1 || len(gg[0].Services) != 1 || gg[0].Services[0].LastExit != nil {
		t.Fatalf("groups = %+v", gg)
	}
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
	index.Add(&Config{
		Name: "shop", Dir: "/nowhere/shop", Path: "/nowhere/shop/" + ConfigName,
		Services: []Service{{Name: "worker", Cmd: "run-worker"}, {Name: "api", Cmd: "run-api"}},
	})

	gg := GroupsWith(nil, index, liveRegistry{})
	if len(gg) != 1 {
		t.Fatalf("groups = %+v", gg)
	}
	byName := map[string]state.Service{}
	for _, s := range gg[0].Services {
		byName[s.Name] = s
	}
	if worker := byName["worker"]; !worker.Running {
		t.Errorf("worker.Running = false, want the live run to be visible")
	} else if worker.PortActual != nil {
		t.Errorf("worker.PortActual = %v, want none: it holds no port", *worker.PortActual)
	} else {
		if worker.PID == nil || *worker.PID != 4242 || worker.RunID == nil || *worker.RunID != "run-worker" {
			t.Errorf("worker runtime = %+v, want pid/run id evidence", worker)
		}
		if worker.StartedAt == nil || *worker.StartedAt != "2026-09-13T10:00:00Z" {
			t.Errorf("worker started_at = %v, want live run timestamp", worker.StartedAt)
		}
	}
	if api := byName["api"]; api.Running {
		t.Errorf("api.Running = true, want only the service with a live run")
	}
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
	index.Add(&Config{
		Name: "shop", Dir: "/nowhere/shop", Path: "/nowhere/shop/" + ConfigName,
		Services: []Service{{Name: "api", Cmd: "old-api"}},
	})
	gg := GroupsWith(nil, index, driftRegistry{})
	if len(gg) != 1 || len(gg[0].Services) != 1 {
		t.Fatalf("groups = %+v", gg)
	}
	svc := gg[0].Services[0]
	if svc.RuntimeCmd == nil || *svc.RuntimeCmd != "new-api" || !svc.ManifestRuntimeMismatch {
		t.Fatalf("runtime drift = %+v, want live command and mismatch", svc)
	}
}
