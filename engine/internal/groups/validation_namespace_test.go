package groups

import (
	"fmt"
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

// The two fingerprint inputs share containment, but not presence: sources: []
// is an opt-out and prepare_inputs without prepare is still an error.
func TestManifestInputPathsShareContainment(t *testing.T) {
	cfg := &Config{Dir: "/synthetic/shop"}
	for _, entry := range []struct{ path, cwd, problem string }{
		{"file", "", ""}, {"../file", "nested", ""}, {"a/../file", "", ""},
		{"", "", "empty"}, {" ", "", "empty"}, {"/absolute", "", "relative"},
		{`\root`, "", "relative"}, {"..", "", "escape"}, {"../../file", "nested", "escape"},
	} {
		t.Run(entry.cwd+":"+entry.path, func(t *testing.T) {
			svc := Service{Name: "api", Prepare: "build", Cwd: entry.cwd, PrepareInputs: []string{entry.path}, Sources: []string{entry.path}}
			for field, got := range map[string][]string{
				"prepare_inputs": cfg.prepareInputProblems(svc, "service api"), "sources": cfg.sourceProblems(svc, "service api"),
			} {
				var want []string
				switch entry.problem {
				case "empty":
					want = []string{"service api: " + field + " has an empty path"}
				case "relative":
					want = []string{fmt.Sprintf("service api: %s path %q must be relative to cwd", field, entry.path)}
				case "escape":
					want = []string{fmt.Sprintf("service api: %s path %q escapes the directory holding oberth.yaml", field, entry.path)}
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s: got %v, want %v", field, got, want)
				}
			}
		})
	}
	noCommand := Service{Prepare: " ", PrepareInputs: []string{"/bad", ""}}
	if got := cfg.prepareInputProblems(noCommand, "service api"); !reflect.DeepEqual(got, []string{"service api: prepare_inputs needs a prepare command"}) {
		t.Errorf("missing prepare = %v", got)
	}
	for _, sources := range [][]string{nil, {}} {
		if got := cfg.sourceProblems(Service{Sources: sources}, "service api"); got != nil {
			t.Errorf("absent/empty sources diagnostics = %#v", got)
		}
	}
}
