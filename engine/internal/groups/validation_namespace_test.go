package groups

import (
	"reflect"
	"testing"
)

// These are in-memory declarations, not running services. A repeated name must
// report each collision, including collisions in more than one reference list.
func TestDeclarationDiagnosticsPreserveEveryNamespace(t *testing.T) {
	cfg := &Config{Name: "shop", Services: []Service{{Name: "api"}, {Name: "api"}, {}},
		Machine:   []MachineRef{{Name: "api", Port: 3306}, {Name: "api", Port: 3307}, {Port: 6379}},
		Workspace: []WorkspaceRef{{Name: "api", Worktree: "../other", Service: "db"}, {Name: "api", Worktree: "../other", Service: "db"}, {}},
	}
	want := []string{
		"machine api: api is already a service in this file",
		"machine api: api is already a service in this file",
		"machine api: duplicate name", "machine[2]: name is empty",
		"service api: duplicate name", "services[2]: name is empty",
		"workspace api: api is already a machine reference in this file",
		"workspace api: api is already a machine reference in this file",
		"workspace api: api is already a service in this file",
		"workspace api: api is already a service in this file",
		"workspace api: duplicate name", "workspace[2]: name is empty",
		"workspace[2]: service is required", "workspace[2]: worktree is required",
	}
	if got := cfg.validate(); !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostics = %#v, want %#v", got, want)
	}
}

func TestDeclarationPortAndPathBoundaries(t *testing.T) {
	cfg := &Config{Name: "shop", Dir: "/synthetic/shop", Services: []Service{
		{Name: "none"}, {Name: "auto", PortAuto: true}, {Name: "first", Port: 1, Cwd: "."},
		{Name: "last", Port: 65535, Cwd: "a/../b"},
	}, Machine: []MachineRef{{Name: "db", Port: 1}, {Name: "cache", Port: 65535}}}
	if got := cfg.validate(); len(got) != 0 {
		t.Fatalf("valid declarations: %v", got)
	}
	cfg.Services = []Service{{Port: -1, Cwd: "../outside", Env: map[string]string{"bad-key": "not-a-secret"}}, {Name: "api", Port: 65536}}
	cfg.Machine = []MachineRef{{}, {Name: "low", Port: -1}, {Name: "high", Port: 65536}}
	want := []string{
		"machine high: port 65536 is out of range 1-65535", "machine low: port -1 is out of range 1-65535",
		"machine[0]: name is empty", "machine[0]: port is required — it is what the reference is matched by",
		"service api: port 65536 is out of range 1-65535",
		"services[0]: cwd \"../outside\" escapes the directory holding oberth.yaml",
		"services[0]: env \"bad-key\" is not a variable name", "services[0]: name is empty",
		"services[0]: port -1 is out of range 1-65535",
	}
	if got := cfg.validate(); !reflect.DeepEqual(got, want) {
		t.Fatalf("invalid declarations = %#v, want %#v", got, want)
	}
}
