package groups

import (
	"testing"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
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
	// Nothing claims 3000: no registry answer, no config, no checkout.
	if resolved[0].Group != nil {
		t.Errorf("port 3000 group = %v, want nil", *resolved[0].Group)
	}
	if got := deref(resolved[1].Group); got != "itest" {
		t.Errorf("port 4000 group = %q, want itest", got)
	}
	if resolved[1].GroupSource == nil || *resolved[1].GroupSource != state.SourceStart {
		t.Errorf("port 4000 group_source = %v, want start", resolved[1].GroupSource)
	}
	// The scanner rows are written back too, so the direct-scan path sees the
	// same attribution.
	if pp[1].Group != "itest" || pp[1].GroupSource != "start" {
		t.Errorf("scanner row = %q/%q, want itest/start", pp[1].Group, pp[1].GroupSource)
	}
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

	if resolved[0].Run == nil {
		t.Fatal("run is null; the registry's attribution never reached the row")
	}
	want := state.Run{ID: "r1", Group: "itest", Name: "web", RootPID: 12}
	if *resolved[0].Run != want {
		t.Errorf("run = %+v, want %+v", *resolved[0].Run, want)
	}
	if resolved[0].DisplayName != "web" {
		t.Errorf("display_name = %q, want the name the run gave it", resolved[0].DisplayName)
	}
	if resolved[0].GroupSource == nil || *resolved[0].GroupSource != state.SourceStart {
		t.Errorf("group_source = %v, want start", resolved[0].GroupSource)
	}
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

	if resolved[0].Run == nil || resolved[0].Run.Name != "web" || resolved[0].Run.RootPID != 9 {
		t.Fatalf("run = %+v, want the stamped run", resolved[0].Run)
	}
	if got := deref(resolved[0].Group); got != "itest" {
		t.Errorf("group = %q, want itest", got)
	}
}

func TestAttributeWithNilArgumentsIsAttributeWithoutTheWorkingDirectory(t *testing.T) {
	pp := []ports.ListeningPort{{Port: 3000, PID: 11, Process: "node"}}
	resolved, index := AttributeWith(pp, nil, nil)
	if len(resolved) != 1 {
		t.Fatalf("resolved %d ports, want 1", len(resolved))
	}
	if resolved[0].Group != nil {
		t.Errorf("group = %v, want nil for a port with no cwd", *resolved[0].Group)
	}
	if len(index.Configs()) != 0 {
		t.Errorf("index picked up %d configs, want none", len(index.Configs()))
	}
}
