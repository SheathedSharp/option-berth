package groups

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
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
	if len(pp) != 3 {
		t.Fatalf("Resolve returned %d ports, want 3", len(pp))
	}

	requireOneGroup(t, Groups(pp, index), state.Group{
		Name: "option-berth", Repo: "option-berth",
		Identity: WorktreeToken(f.repo), Source: state.SourceAuto,
		RootDir: &f.repo, Status: "running", Members: []int{8123, 8124},
		Services: []state.Service{}, Machine: []state.MachineRef{},
	})
	if WorktreeTokenFor(f.repoSub) != WorktreeToken(f.repo) {
		t.Errorf("a nested path has a different token than the checkout root")
	}
}

// A machine reference is rendered against what the whole scan sees, not against
// the group's own members: the port a project depends on belongs to the
// machine's tier, so no member of this group holds it.
func TestGroupsRenderMachineReferences(t *testing.T) {
	f, index, rows := machineReferenceWorld(t)
	config := filepath.Join(f.repo, ConfigName)
	requireOneGroup(t, Groups(rows, index), state.Group{
		Name: "option-berth", Repo: "option-berth",
		Identity: WorktreeToken(f.repo), Source: state.SourceFile,
		RootDir: &f.repo, ConfigPath: &config, Status: "running",
		Members:  []int{8123},
		Services: []state.Service{},
		Machine: []state.MachineRef{
			{Name: "mysql", Port: 3306, Listening: true, ListeningKnown: true},
			{Name: "ghost", Port: 9999, Listening: false, ListeningKnown: true},
		},
	})
}

// A narrow table (the Darwin lsof fallback) cannot see other users' sockets.
// A port it did show stays a fact; a missing one is unknown, not absent.
func TestMachineReferencesPublishUnknownUnderANarrowTable(t *testing.T) {
	f, index, rows := machineReferenceWorld(t)
	config := filepath.Join(f.repo, ConfigName)
	requireOneGroup(t, GroupsWith(rows, index, NoRuns{}, true), state.Group{
		Name: "option-berth", Repo: "option-berth",
		Identity: WorktreeToken(f.repo), Source: state.SourceFile,
		RootDir: &f.repo, ConfigPath: &config, Status: "running",
		Members:  []int{8123},
		Services: []state.Service{},
		Machine: []state.MachineRef{
			{Name: "mysql", Port: 3306, Listening: true, ListeningKnown: true},
			{Name: "ghost", Port: 9999, Listening: false, ListeningKnown: false},
		},
	})
}

func TestGroupsStatusFromServices(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.repo, ConfigName),
		"name: option-berth\nservices:\n  - name: api\n    port: 8000\n  - name: web\n    port: 5173\n  - name: worker\n")
	config := filepath.Join(f.repo, ConfigName)
	apiHash := "83e7evap76l8mgm1uui40qv6387a8cbfieiaeiu8k1ql7696qitg"
	webHash := "e3hr8249vm80p51bph2fn45bl5ll5u7j72ti7e3gk8dsnludrlb0"
	workerHash := "qebi5bgj4l1b94b9nnd6330hrsntb3kvplijkhqle2ctmpeqvf70"

	tests := []struct {
		name             string
		listening        []state.Port
		status           string
		members          []int
		running          int
		api, web, worker state.Service
	}{
		{
			name: "nothing running", status: "stopped", members: []int{},
			api:    publishedService("api", intPtr(8000), nil, nil, apiHash, false, nil),
			web:    publishedService("web", intPtr(5173), nil, nil, webHash, false, nil),
			worker: publishedService("worker", nil, nil, nil, workerHash, false, nil),
		},
		{
			name: "one of three", listening: []state.Port{nativePort(8000, f.repo)},
			status: "partial", members: []int{8000}, running: 1,
			api:    publishedService("api", intPtr(8000), nil, nil, apiHash, true, intPtr(8000)),
			web:    publishedService("web", intPtr(5173), nil, nil, webHash, false, nil),
			worker: publishedService("worker", nil, nil, nil, workerHash, false, nil),
		},
		{
			name: "all three, the last matched by name",
			listening: []state.Port{
				nativePort(8000, f.repo), nativePort(5173, f.repo),
				{Port: 9000, Cwd: f.repo, DisplayName: "worker"},
			},
			status: "running", members: []int{5173, 8000, 9000}, running: 3,
			api:    publishedService("api", intPtr(8000), nil, nil, apiHash, true, intPtr(8000)),
			web:    publishedService("web", intPtr(5173), nil, nil, webHash, true, intPtr(5173)),
			worker: publishedService("worker", nil, nil, nil, workerHash, true, intPtr(9000)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index := NewIndex()
			index.Observe(f.repo)
			requireOneGroup(t, Groups(Resolve(tt.listening, NoRuns{}, index), index), state.Group{
				Name: "option-berth", Repo: "option-berth",
				Identity: WorktreeToken(f.repo), Source: state.SourceFile,
				RootDir: &f.repo, ConfigPath: &config, Status: tt.status,
				Members: tt.members, Services: []state.Service{tt.api, tt.web, tt.worker},
				Machine: []state.MachineRef{}, RunningServices: tt.running,
			})
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
	config := filepath.Join(f.repo, ConfigName)
	svc := publishedService("api", intPtr(8000), optional("/healthz"), nil, "iqnjbtiqi5vusnemrfqcdfndktndm5npl9kvn68rnujtp9r9e8m0", true, intPtr(8000))
	svc.HealthStatus = health
	requireOneGroup(t, Groups(Resolve([]state.Port{row}, NoRuns{}, index), index), state.Group{
		Name: "option-berth", Repo: "option-berth",
		Identity: WorktreeToken(f.repo), Source: state.SourceFile,
		RootDir: &f.repo, ConfigPath: &config, Status: "running",
		Members: []int{8000}, Services: []state.Service{svc},
		Machine: []state.MachineRef{}, RunningServices: 1,
	})
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
	if len(pp) != 2 {
		t.Fatalf("Resolve returned %d ports, want 2", len(pp))
	}
	config := filepath.Join(f.repo, ConfigName)
	got := Groups(pp, index)
	requireOneGroup(t, got, state.Group{
		Name: "option-berth", Repo: "option-berth",
		Identity: WorktreeToken(f.repo), Source: state.SourceFile,
		RootDir: &f.repo, ConfigPath: &config, Status: "partial",
		Members: []int{8000},
		Services: []state.Service{
			publishedService("api", intPtr(8000), optional("/health"), []string{"db"}, "b57bgg60uerbopdku284qf6cumljqlc48dklv0dp6f412l3t63mg", true, intPtr(8000)),
			publishedService("db", intPtr(5432), nil, nil, "5ugkq8sl6mnho9sn89fp8u0puk65va4d6gp06i36p80pplunht2g", false, nil),
		},
		Machine: []state.MachineRef{}, RunningServices: 1,
	})

	sch := compileDefinition(t, "Group")
	for _, g := range got {
		if err := sch.Validate(roundTrip(t, g)); err != nil {
			t.Errorf("group %s does not validate:\n%v", g.Name, err)
		}
	}
}

func machineReferenceWorld(t *testing.T) (fixture, *Index, []state.Port) {
	t.Helper()
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
	return f, index, rows
}

func publishedService(name string, port *int, health *string, depends []string, hash string, running bool, actual *int) state.Service {
	if depends == nil {
		depends = []string{}
	}
	return state.Service{
		Name: name, PrepareInputs: []string{}, ReadyTimeout: "30s",
		Port: port, Health: health, ManifestHash: optional(hash),
		DependsOn: depends, Running: running, PortActual: actual,
		LogPath: optional(filepath.Join(paths.Logs(), "option-berth", name+".log")),
	}
}

func requireOneGroup(t *testing.T, got []state.Group, want state.Group) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("groups = %+v, want one", got)
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("group = %#v, want %#v", got[0], want)
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
