package groups

import (
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/internal/state"
)

type snapshotRegistry struct {
	NoRuns
	calls int
	facts servicefacts.Snapshot
}

func (r *snapshotRegistry) ServiceFacts() servicefacts.Snapshot {
	r.calls++
	return r.facts
}

func TestGroupsCaptureOneSnapshotForAllWorktrees(t *testing.T) {
	index := NewIndex()
	for _, name := range []string{"left", "right"} {
		index.Add(&Config{Name: name, Dir: "/nonexistent/" + name, Services: []Service{{Name: "worker", Cmd: "worker"}}})
	}
	reg := &snapshotRegistry{facts: servicefacts.Capture([]servicefacts.RunRecord{{
		Key: servicefacts.Key{Group: "left", Service: "worker"}, Run: state.ServiceRun{ID: "left-run", PID: 42},
	}}, []servicefacts.ExitRecord{{
		Key: servicefacts.Key{Group: "right", Service: "worker"}, Exit: state.ServiceExit{RunID: "right-exit", Code: 1},
	}})}
	got := GroupsWith(nil, index, reg)
	if reg.calls != 1 || len(got) != 2 {
		t.Fatalf("capture calls=%d groups=%+v", reg.calls, got)
	}
	left, right := got[0].Services[0], got[1].Services[0]
	if !left.Running || left.RunID == nil || *left.RunID != "left-run" || left.PortActual != nil || left.LastExit != nil {
		t.Fatalf("left worker = %+v", left)
	}
	if right.Running || right.RunID != nil || right.LastExit == nil || right.LastExit.RunID != "right-exit" {
		t.Fatalf("right worker = %+v", right)
	}
}

func TestGroupsDoNotJoinAnotherRunsListener(t *testing.T) {
	index := NewIndex()
	index.Add(&Config{Name: "shop", Dir: "/nonexistent/shop", Services: []Service{
		{Name: "api", Port: 8000}, {Name: "worker"},
	}})
	group := "shop"
	listeners := []state.Port{{Group: &group, Port: 8000, PID: 77, Run: &state.Run{Group: group, Name: "worker", ID: "worker-run"}}}
	got := GroupsWith(listeners, index, NoRuns{})[0]
	if got.Services[0].Running || got.Services[0].PID != nil || got.Services[0].PortActual != nil {
		t.Fatalf("api claimed another service's listener: %+v", got.Services[0])
	}
	if !got.Services[1].Running || got.Services[1].RunID != nil {
		t.Fatalf("observed listener should not invent registry-owned identity: %+v", got.Services[1])
	}
}

func TestServiceRowsCopyHealthAndReuseManifestFingerprint(t *testing.T) {
	cfg := &Config{Dir: "/nonexistent/shop", Services: []Service{{Name: "api", Cmd: "serve", Port: 8000}}}
	group := "shop"
	health := &state.Health{Configured: true, Status: state.HealthOK}
	listeners := []state.Port{{Group: &group, Port: 8000, PID: 77, Health: health}}
	for _, mismatch := range []bool{false, true} {
		hash := ServiceSpecHash(cfg.Services[0])
		if mismatch {
			hash = strings.Repeat("d", SpecHashLen)
		}
		facts := servicefacts.Capture([]servicefacts.RunRecord{{
			Key: servicefacts.Key{Group: group, Service: "api"},
			Run: state.ServiceRun{ID: "run", PID: 42, Cmd: " serve ", Cwd: cfg.Dir, SpecHash: hash},
		}}, nil)
		row := serviceRowsWithFacts(cfg, group, servicefacts.NewResolver(facts, listeners))[0]
		if row.ManifestRuntimeMismatch != mismatch || row.PID == nil || *row.PID != 42 || row.HealthStatus == nil {
			t.Fatalf("row = %+v, mismatch=%v", row, mismatch)
		}
		row.HealthStatus.Reason = "changed presentation"
		if health.Reason != "" {
			t.Fatal("output aliases input health")
		}
	}
}

// TestPreUpgradeSpecHashIsNotDrift pins the upgrade window: a run registered
// before the hash encoding changed carries the old format, which differs from
// the current manifest hash by construction. That is not manifest drift — the
// command and the cwd are unchanged — and reporting it would send the user to
// restart every service that survived a daemon upgrade for nothing.
func TestPreUpgradeSpecHashIsNotDrift(t *testing.T) {
	cfg := &Config{Name: "shop", Dir: "/nonexistent/shop", Path: "/nonexistent/shop/" + ConfigName,
		Services: []Service{{Name: "api", Cmd: "serve"}}}
	index := NewIndex()
	index.Add(cfg)
	legacy := &snapshotRegistry{facts: servicefacts.Capture([]servicefacts.RunRecord{{
		Key: servicefacts.Key{Group: "shop", Service: "api"},
		// A hex-encoded digest: the pre-upgrade format, 64 characters.
		Run: state.ServiceRun{ID: "run", PID: 7, Cmd: "serve", Cwd: cfg.Dir,
			SpecHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}}, nil)}
	svc := GroupsWith(nil, index, legacy)[0].Services[0]
	if svc.RuntimeSpecHash == nil {
		t.Fatalf("runtime spec hash = nil, want the recorded value published")
	}
	if svc.ManifestRuntimeMismatch {
		t.Fatalf("pre-upgrade hash reported as drift: %+v", svc)
	}
}
