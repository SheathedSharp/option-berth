package groups

import (
	"reflect"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// runSet is a Registry that answers for one port number.
type runSet struct {
	port  int
	id    string
	group string
	name  string
}

func (r runSet) Run(p state.Port) (state.Run, bool) {
	if p.Port != r.port {
		return state.Run{}, false
	}
	return state.Run{ID: r.id, Group: r.group, Name: r.name, RootPID: p.PID}, true
}

func requireAttributedPort(t *testing.T, got, want state.Port) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("port %d = %#v, want %#v", want.Port, got, want)
	}
}

func requireScannerStamp(t *testing.T, got ports.ListeningPort, group, source, runID, runGroup, tag string, rootPID int) {
	t.Helper()
	if got.Group != group || got.GroupSource != source || got.RunID != runID || got.RunGroup != runGroup || got.Tag != tag || got.RunRootPID != rootPID {
		t.Errorf("scanner row = group %q source %q run %q/%q/%q pid %d, want %q/%q/%q/%q/%q/%d",
			got.Group, got.GroupSource, got.RunID, got.RunGroup, got.Tag, got.RunRootPID,
			group, source, runID, runGroup, tag, rootPID)
	}
}

func TestAttributeWithAppliesTheRegistry(t *testing.T) {
	pp := []ports.ListeningPort{
		{Port: 3000, PID: 11, Process: "node", Command: "node server.js"},
		{Port: 4000, PID: 12, Process: "python3", Command: "python3 -m http.server"},
	}

	resolved, index := AttributeWith(pp,
		runSet{port: 4000, id: "r1", group: "itest", name: "web"},
		nil)
	if index == nil {
		t.Fatal("AttributeWith returned a nil index")
	}
	if len(index.Configs()) != 0 {
		t.Errorf("index picked up %d configs, want none", len(index.Configs()))
	}
	if len(resolved) != 2 {
		t.Fatalf("resolved %d ports, want 2", len(resolved))
	}

	// Nothing claims 3000: no registry answer, no config, no checkout.
	group := "itest"
	source := state.SourceStart
	run := state.Run{ID: "r1", Group: "itest", Name: "web", RootPID: 12}
	requireAttributedPort(t, resolved[0], state.Port{
		Host: "localhost", Port: 3000, URL: "http://localhost:3000",
		PID: 11, Process: "node", DisplayName: "server.js",
		Command: "node server.js", Type: state.TypeUser,
	})
	requireAttributedPort(t, resolved[1], state.Port{
		Host: "localhost", Port: 4000, URL: "http://localhost:4000",
		PID: 12, Process: "python3", DisplayName: "web",
		Command: "python3 -m http.server", Type: state.TypeUser,
		Group: &group, GroupSource: &source, Run: &run,
	})
	// The scanner rows are written back too, so the direct-scan path sees the
	// same attribution.
	requireScannerStamp(t, pp[0], "", "", "", "", "", 0)
	requireScannerStamp(t, pp[1], "itest", "start", "r1", "itest", "web", 12)
}

// TestAttributeWithPublishesTheRegistrysRun pins the invariant the daemon
// broke: whatever the registry says owns a port has to reach the published row
// as `run` and as the name clients render, not only as the group. A registry
// answer that set `group_source: start` next to a null `run` is what a
// `oberth start` racing a scan tick used to publish.
func TestAttributeWithPublishesTheRegistrysRun(t *testing.T) {
	pp := []ports.ListeningPort{
		{Port: 4000, PID: 12, Process: "listener", Command: "/tmp/listener 4000"},
	}

	resolved, _ := AttributeWith(pp, runSet{port: 4000, id: "r1", group: "itest", name: "web"}, nil)
	if len(resolved) != 1 {
		t.Fatalf("resolved %d ports, want 1", len(resolved))
	}
	group := "itest"
	source := state.SourceStart
	run := state.Run{ID: "r1", Group: "itest", Name: "web", RootPID: 12}
	requireAttributedPort(t, resolved[0], state.Port{
		Host: "localhost", Port: 4000, URL: "http://localhost:4000",
		PID: 12, Process: "listener", DisplayName: "web",
		Command: "/tmp/listener 4000", Type: state.TypeUser,
		Group: &group, GroupSource: &source, Run: &run,
	})
	requireScannerStamp(t, pp[0], "itest", "start", "r1", "itest", "web", 12)
}

// TestAttributeWithKeepsStampedRunFieldsOnTheRow pins the PortRuns contract:
// it reads the fields attribution already stamped, so downstream adapters
// (the direct scan's service-fact registry) see the same run the resolver
// used.
func TestAttributeWithKeepsStampedRunFieldsOnTheRow(t *testing.T) {
	pp := []ports.ListeningPort{
		{Port: 4000, PID: 12, Process: "listener", Tag: "web", RunGroup: "itest", RunID: "r1", RunRootPID: 9},
	}

	resolved, _ := AttributeWith(pp, PortRuns{}, nil)
	if len(resolved) != 1 {
		t.Fatalf("resolved %d ports, want 1", len(resolved))
	}
	group := "itest"
	source := state.SourceStart
	run := state.Run{ID: "r1", Group: "itest", Name: "web", RootPID: 9}
	requireAttributedPort(t, resolved[0], state.Port{
		Host: "localhost", Port: 4000, URL: "http://localhost:4000",
		PID: 12, Process: "listener", DisplayName: "web", Type: state.TypeUser,
		Group: &group, GroupSource: &source, Run: &run,
	})
	requireScannerStamp(t, pp[0], "itest", "start", "r1", "itest", "web", 9)
}

func TestAttributeWithNilArgumentsIsAttributeWithoutTheWorkingDirectory(t *testing.T) {
	pp := []ports.ListeningPort{{Port: 3000, PID: 11, Process: "node"}}
	resolved, index := AttributeWith(pp, nil, nil)
	if index == nil {
		t.Fatal("AttributeWith returned a nil index")
	}
	if len(index.Configs()) != 0 {
		t.Errorf("index picked up %d configs, want none", len(index.Configs()))
	}
	if len(resolved) != 1 {
		t.Fatalf("resolved %d ports, want 1", len(resolved))
	}
	requireAttributedPort(t, resolved[0], state.Port{
		Host: "localhost", Port: 3000, URL: "http://localhost:3000",
		PID: 11, Process: "node", DisplayName: "node", Type: state.TypeUser,
	})
	requireScannerStamp(t, pp[0], "", "", "", "", "", 0)
}
