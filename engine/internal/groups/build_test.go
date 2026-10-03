package groups

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestGroupsFromPortsOnly(t *testing.T) {
	f := newFixture(t)
	index := NewIndex()
	index.Observe(f.repoSub)

	pp := Resolve([]state.Port{
		nativePort(8123, f.repoSub),
		nativePort(8124, f.repo),
		nativePort(3000, ""),
	}, NoRuns{}, index)

	got := Groups(pp, index)
	if len(got) != 1 {
		t.Fatalf("groups = %+v, want one", got)
	}
	g := got[0]
	if g.Name != "option-berth" || g.Source != state.SourceAuto || g.Status != "running" {
		t.Fatalf("group = %+v", g)
	}
	if len(g.Members) != 2 || g.Members[0] != 8123 || g.Members[1] != 8124 {
		t.Errorf("members = %v, want [8123 8124] sorted", g.Members)
	}
	if g.RootDir == nil || *g.RootDir != f.repo {
		t.Errorf("root_dir = %v, want %q", g.RootDir, f.repo)
	}
	if g.ConfigPath != nil {
		t.Errorf("config_path = %v, want null with no config", g.ConfigPath)
	}
}

// A machine reference is rendered against what the whole scan sees, not against
// the group's own members: the port a project depends on belongs to the
// machine's tier, so no member of this group holds it.
func TestGroupsRenderMachineReferences(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.repo, ConfigName), `name: option-berth
machine:
  - name: mysql
    port: 3306
  - name: ghost
    port: 9999
`)
	index := NewIndex()
	index.Observe(f.repo)

	rows := Resolve([]state.Port{
		{Port: 3306},
		nativePort(8123, f.repo),
	}, NoRuns{}, index)

	got := Groups(rows, index)
	if len(got) != 1 {
		t.Fatalf("groups = %+v, want one", got)
	}
	m := got[0].Machine
	if len(m) != 2 {
		t.Fatalf("machine = %+v, want both references", m)
	}
	if m[0].Name != "mysql" || m[0].Port != 3306 || !m[0].Listening {
		t.Errorf("mysql = %+v, want it listening on 3306", m[0])
	}
	if m[0].Unit != nil {
		t.Errorf("mysql unit = %v, want no machine service-manager inference", m[0].Unit)
	}
	if m[1].Name != "ghost" || m[1].Listening || m[1].Unit != nil {
		t.Errorf("ghost = %+v, want it not listening and with no unit", m[1])
	}
}

func TestGroupsStatusFromServices(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.repo, ConfigName),
		"name: option-berth\nservices:\n  - name: api\n    port: 8000\n  - name: web\n    port: 5173\n  - name: worker\n")

	tests := []struct {
		name       string
		listening  []state.Port
		wantStatus string
		wantUp     []string
	}{
		{"nothing running", nil, "stopped", nil},
		{
			"one of three", []state.Port{nativePort(8000, f.repo)},
			"partial", []string{"api"},
		},
		{
			"all three, the last matched by name",
			[]state.Port{
				nativePort(8000, f.repo), nativePort(5173, f.repo),
				{Port: 9000, Cwd: f.repo, DisplayName: "worker"},
			},
			"running", []string{"api", "web", "worker"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index := NewIndex()
			index.Observe(f.repo)
			pp := Resolve(tt.listening, NoRuns{}, index)
			got := Groups(pp, index)
			if len(got) != 1 {
				t.Fatalf("groups = %+v", got)
			}
			g := got[0]
			if g.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", g.Status, tt.wantStatus)
			}
			if g.Source != state.SourceFile {
				t.Errorf("source = %q, want file", g.Source)
			}
			if g.ConfigPath == nil || *g.ConfigPath != filepath.Join(f.repo, ConfigName) {
				t.Errorf("config_path = %v", g.ConfigPath)
			}
			if len(g.Services) != 3 {
				t.Fatalf("services = %+v", g.Services)
			}
			var up []string
			for _, s := range g.Services {
				if s.Running {
					up = append(up, s.Name)
					if s.PortActual == nil {
						t.Errorf("service %s is running with no port_actual", s.Name)
					}
				}
			}
			if len(up) != len(tt.wantUp) {
				t.Fatalf("running services = %v, want %v", up, tt.wantUp)
			}
			for i := range up {
				if up[i] != tt.wantUp[i] {
					t.Fatalf("running services = %v, want %v", up, tt.wantUp)
				}
			}
		})
	}
}

func TestGroupsJoinConfiguredHealthToService(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.repo, ConfigName),
		"name: option-berth\nservices:\n  - name: api\n    port: 8000\n    health: /healthz\n")
	index := NewIndex()
	index.Observe(f.repo)
	health := &state.Health{Status: state.HealthFail, Code: 503, Reason: "unhealthy", Configured: true}
	row := nativePort(8000, f.repo)
	row.Health = health
	got := Groups(Resolve([]state.Port{row}, NoRuns{}, index), index)
	if len(got) != 1 || len(got[0].Services) != 1 {
		t.Fatalf("groups = %+v", got)
	}
	svc := got[0].Services[0]
	if svc.HealthStatus == nil || svc.HealthStatus.Status != state.HealthFail || svc.HealthStatus.Code != 503 {
		t.Fatalf("health_status = %+v, want the configured listener verdict", svc.HealthStatus)
	}
}

// TestGroupsValidateAgainstTheSchema keeps the group builder inside the
// published contract: whatever `oberth groups --json` prints must satisfy the
// checked-in Group definition.
func TestGroupsValidateAgainstTheSchema(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.repo, ConfigName),
		"name: option-berth\nservices:\n  - name: api\n    port: 8000\n    health: /health\n    depends_on: [db]\n  - name: db\n    port: 5432\n")
	index := NewIndex()
	index.Observe(f.repo)
	pp := Resolve([]state.Port{nativePort(8000, f.repo), nativePort(3000, "")}, NoRuns{}, index)

	sch := compileDefinition(t, "Group")
	for _, g := range Groups(pp, index) {
		if err := sch.Validate(roundTrip(t, g)); err != nil {
			t.Errorf("group %s does not validate:\n%v", g.Name, err)
		}
	}
}

func compileDefinition(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "schema", "protocol.schema.json")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%v (run `go generate ./...`)", err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("protocol.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("protocol.schema.json#/definitions/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return sch
}
