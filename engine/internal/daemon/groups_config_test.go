package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

const configFixture = `# demo project
name: demo
services:
  # the database goes first
  - name: db
    cmd: postgres
    port: 5432
  - name: api
    cmd: uv run api
    port: 8000
    health: /health
    depends_on: [db]
`

// configHarness gives a test a daemon whose index already holds one config,
// seen the way the scanner sees one: through a listening process's cwd.
func configHarness(t *testing.T, ctx context.Context) (*testHarness, string, string) {
	t.Helper()
	dir := resolvedDir(t, t.TempDir())
	path := filepath.Join(dir, groups.ConfigName)
	if err := os.WriteFile(path, []byte(configFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, ctx)
	h.setRows(ports.ListeningPort{
		Port: 5432, BindAddress: "127.0.0.1", PID: 501, Process: "postgres", Cwd: dir,
	})
	if _, err := h.loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatalf("priming the scanner: %v", err)
	}
	return h, dir, path
}

func resolvedDir(t *testing.T, dir string) string {
	t.Helper()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return dir
}

// configCase is one daemon whose index already holds the fixture manifest, a
// client bound to it, and the directory and file the fixture was written to.
// Cancelling the test's context shuts the daemon down; the helper arranges that
// itself, so a test body starts at the call it means to make.
type configCase struct {
	harness *testHarness
	client  *testClient
	dir     string
	path    string
}

// newConfigCase builds the fixture daemon the config handlers are exercised
// against.
func newConfigCase(t *testing.T) *configCase {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	harness, dir, path := configHarness(t, ctx)
	return &configCase{
		harness: harness,
		client:  harness.dial(ctx),
		dir:     dir,
		path:    path,
	}
}

// newBareConfigCase is newConfigCase without the fixture manifest: the test
// writes its own file, so the daemon has never scanned the directory.
func newBareConfigCase(t *testing.T) *configCase {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// The directory is claimed before the harness so the harness stops before
	// the directory it indexed is removed.
	dir := resolvedDir(t, t.TempDir())
	harness := newHarness(t, ctx)
	return &configCase{harness: harness, client: harness.dial(ctx), dir: dir}
}

// assertFileHas fails unless the manifest on disk contains want. The hint says
// what losing it would mean, so a failure names the consequence rather than
// just the missing substring.
func assertFileHas(t *testing.T, path, want, hint string) {
	t.Helper()
	if contents := readFixture(t, path); !strings.Contains(contents, want) {
		t.Fatalf("%s (%q missing):\n%s", hint, want, contents)
	}
}

// readFixture returns the manifest's bytes as they are on disk right now,
// ending the test when it cannot be read. Several cases compare the file before
// and after a handler, and a read failure there is always a broken fixture
// rather than a behaviour under test.
func readFixture(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(contents)
}

// TestGroupsConfigGetByName returns the file as the resolver publishes it,
// joined against what is actually running.
func TestGroupsConfigGetByName(t *testing.T) {
	fixture := newConfigCase(t)
	c, path := fixture.client, fixture.path

	name := "demo"
	var res rpc.GroupsConfigGetResult
	if e := c.call("groups.config.get", rpc.GroupsConfigGetParams{Name: &name}, &res); e != nil {
		t.Fatalf("groups.config.get: %v", e)
	}
	if res.Path != path {
		t.Errorf("path = %q, want %q", res.Path, path)
	}
	if res.Config.Name != "demo" || len(res.Config.Services) != 2 {
		t.Fatalf("config = %+v", res.Config)
	}
	db, api := res.Config.Services[0], res.Config.Services[1]
	if db.Name != "db" || !db.Running || db.PortActual == nil || *db.PortActual != 5432 {
		t.Errorf("db row = %+v, want it running on 5432", db)
	}
	if api.Health == nil || *api.Health != "/health" || api.Running {
		t.Errorf("api row = %+v, want /health and not running", api)
	}
	if api.Description != nil || api.Icon != nil || api.Color != nil {
		t.Errorf("api metadata should be null: %+v", api)
	}
}

// TestGroupsConfigGetByPath also indexes a file the daemon has never scanned.
func TestGroupsConfigGetByPath(t *testing.T) {
	fixture := newBareConfigCase(t)
	c, dir := fixture.client, fixture.dir

	path := filepath.Join(dir, groups.ConfigName)
	if err := os.WriteFile(path, []byte(configFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	var res rpc.GroupsConfigGetResult
	if e := c.call("groups.config.get", rpc.GroupsConfigGetParams{Path: &path}, &res); e != nil {
		t.Fatalf("groups.config.get: %v", e)
	}
	if res.Config.Name != "demo" {
		t.Fatalf("config = %+v", res.Config)
	}
}

func TestGroupsConfigGetNotFound(t *testing.T) {
	fixture := newConfigCase(t)
	c, dir := fixture.client, fixture.dir

	name := "nope"
	if e := c.call("groups.config.get", rpc.GroupsConfigGetParams{Name: &name}, nil); e == nil {
		t.Fatal("an unknown group name should not resolve")
	} else if e.Data.Code != "not_found" {
		t.Errorf("error = %+v, want not_found", e)
	}

	missing := filepath.Join(dir, "sub", groups.ConfigName)
	if e := c.call("groups.config.get", rpc.GroupsConfigGetParams{Path: &missing}, nil); e == nil {
		t.Fatal("a missing path should not resolve")
	} else if e.Data.Code != "not_found" {
		t.Errorf("error = %+v, want not_found", e)
	}

	if e := c.call("groups.config.get", rpc.GroupsConfigGetParams{}, nil); e == nil {
		t.Fatal("neither name nor path should be invalid params")
	} else if e.Data.Code != "invalid_params" {
		t.Errorf("error = %+v, want invalid_params", e)
	}
}

// TestGroupsConfigSetWritesAndPublishes is the round trip the desktop editor
// makes: patch a service, keep the comments, and see the change in a delta
// without asking for it.
func TestGroupsConfigSetWritesAndPublishes(t *testing.T) {
	fixture := newConfigCase(t)
	c, path := fixture.client, fixture.path

	// Subscribe first, so the delta the write publishes has somewhere to go.
	var snap state.Snapshot
	if e := c.call("state.subscribe", rpc.StateSubscribeParams{}, &snap); e != nil {
		t.Fatalf("state.subscribe: %v", e)
	}

	// The handler publishes before it replies, so the delta is already on the
	// wire when the response arrives: read both together.
	var res rpc.GroupsConfigSetResult
	edited := callCollectingGroups(t, c, "groups.config.set", rpc.GroupsConfigSetParams{
		Path: path,
		Services: []groups.ServiceEdit{{
			Name: "db",
			Patch: groups.ServicePatch{}.
				SetString(groups.FieldHealth, "/").
				SetString(groups.FieldColor, "blue"),
		}},
	}, &res)
	if !res.OK || len(res.Affected) != 1 || res.Affected[0] != "db" {
		t.Errorf("result = %+v", res.MutationResult)
	}
	db := res.Config.Services[0]
	if db.Health == nil || *db.Health != "/" || db.Color == nil || *db.Color != "blue" {
		t.Fatalf("returned row = %+v", db)
	}

	assertFileHas(t, path, "# the database goes first", "the write dropped a comment the editor must preserve")
	assertFileHas(t, path, "color: blue", "the patch never reached the file")

	// The delta carries the edited group.
	g, ok := edited["demo"]
	if !ok {
		g2 := waitForGroup(t, c, "demo")
		if g2 == nil {
			t.Fatal("no groups delta after the write")
		}
		g = *g2
	}
	if g.Services[0].Color == nil || *g.Services[0].Color != "blue" {
		t.Errorf("the published group is stale: %+v", g.Services[0])
	}
}

// callCollectingGroups makes one call and returns every group that arrived in a
// delta while it was in flight, keyed by name.
func callCollectingGroups(t *testing.T, c *testClient, method string, params, out any) map[string]state.Group {
	t.Helper()
	id := c.send(method, params)
	seen := map[string]state.Group{}
	for {
		m := c.read()
		if m.Method == rpc.MethodStateDelta {
			var d state.Delta
			if err := json.Unmarshal(m.Params, &d); err != nil {
				t.Fatalf("decoding delta: %v", err)
			}
			for _, g := range append(append([]state.Group{}, d.Groups.Added...), d.Groups.Updated...) {
				seen[g.Name] = g
			}
			continue
		}
		if !m.IsResponse() || string(m.ID) != id {
			continue
		}
		if m.Error != nil {
			t.Fatalf("%s: %v", method, m.Error)
		}
		if out != nil && len(m.Result) > 0 {
			if err := json.Unmarshal(m.Result, out); err != nil {
				t.Fatalf("decoding %s result: %v", method, err)
			}
		}
		return seen
	}
}

func TestGroupsConfigSetErrors(t *testing.T) {
	fixture := newConfigCase(t)
	c, dir, path := fixture.client, fixture.dir, fixture.path
	before := readFixture(t, path)

	// A service the file does not declare.
	if e := c.call("groups.config.set", rpc.GroupsConfigSetParams{
		Path:     path,
		Services: []groups.ServiceEdit{{Name: "worker", Patch: groups.ServicePatch{}.SetString(groups.FieldIcon, "gear")}},
	}, nil); e == nil {
		t.Fatal("patching an unknown service should fail")
	} else if e.Data.Code != "not_found" {
		t.Errorf("error = %+v, want not_found", e)
	}

	// A patch that would make the file invalid.
	e := c.call("groups.config.set", rpc.GroupsConfigSetParams{
		Path:     path,
		Services: []groups.ServiceEdit{{Name: "api", Patch: groups.ServicePatch{}.SetPort(70000)}},
	}, nil)
	if e == nil {
		t.Fatal("an invalid port should fail")
	}
	if e.Code != rpc.CodeInvalidConfig || e.Data.Code != "invalid_config" {
		t.Errorf("error = %+v, want 1006 invalid_config", e)
	}
	if !strings.Contains(e.Data.Detail, "70000") {
		t.Errorf("detail should name the bad value: %q", e.Data.Detail)
	}

	// Anything that is not a .oberth.yaml is refused outright.
	if e := c.call("groups.config.set", rpc.GroupsConfigSetParams{
		Path:     filepath.Join(dir, "docker-compose.yml"),
		Services: []groups.ServiceEdit{{Name: "api", Patch: groups.ServicePatch{}.SetString(groups.FieldIcon, "x")}},
	}, nil); e == nil || e.Data.Code != "invalid_params" {
		t.Errorf("error = %+v, want invalid_params", e)
	}

	after := readFixture(t, path)
	if after != before {
		t.Errorf("a failed write changed the file:\n%s", after)
	}
}

// TestGroupsReloadPicksUpANewFile: the index is long-lived, so this is the
// method that makes a project created after the daemon started a group.
func TestGroupsReloadPicksUpANewFile(t *testing.T) {
	fixture := newConfigCase(t)
	c, dir := fixture.client, fixture.dir

	sub := filepath.Join(dir, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, groups.ConfigName),
		[]byte("name: nested\nservices:\n  - name: worker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The daemon only knows the parent directory, so nothing has seen this
	// file yet; the reload has to walk the roots it remembers.
	fixture.harness.loop.ObserveConfig(sub)

	var res rpc.GroupsReloadResult
	if e := c.call("groups.reload", rpc.Empty{}, &res); e != nil {
		t.Fatalf("groups.reload: %v", e)
	}
	if res.Loaded != 2 || len(res.Errors) != 0 {
		t.Fatalf("reload = %+v", res)
	}

	name := "nested"
	var got rpc.GroupsConfigGetResult
	if e := c.call("groups.config.get", rpc.GroupsConfigGetParams{Name: &name}, &got); e != nil {
		t.Fatalf("the new config is not in the index: %v", e)
	}
	if got.Config.Services[0].Name != "worker" {
		t.Errorf("config = %+v", got.Config)
	}
}

// TestGroupsReloadReportsBrokenFiles: a file that does not validate is listed,
// never fatal.
func TestGroupsReloadReportsBrokenFiles(t *testing.T) {
	fixture := newConfigCase(t)
	c, path := fixture.client, fixture.path

	if err := os.WriteFile(path, []byte("name: two words\nservices:\n  - name: api\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var res rpc.GroupsReloadResult
	if e := c.call("groups.reload", rpc.Empty{}, &res); e != nil {
		t.Fatalf("groups.reload: %v", e)
	}
	if res.Loaded != 0 || len(res.Errors) != 1 {
		t.Fatalf("reload = %+v", res)
	}
	if res.Errors[0].Path != path || !strings.Contains(res.Errors[0].Error, "whitespace") {
		t.Errorf("error row = %+v", res.Errors[0])
	}
}

// waitForGroup reads notifications until a delta carries the named group.
func waitForGroup(t *testing.T, c *testClient, name string) *state.Group {
	t.Helper()
	for i := 0; i < 20; i++ {
		m := c.read()
		if m.Method != rpc.MethodStateDelta {
			continue
		}
		var d state.Delta
		if err := json.Unmarshal(m.Params, &d); err != nil {
			t.Fatalf("decoding delta: %v", err)
		}
		for _, g := range append(append([]state.Group{}, d.Groups.Added...), d.Groups.Updated...) {
			if g.Name == name {
				out := g
				return &out
			}
		}
	}
	return nil
}

// TestGroupsConfigSetAddsRenamesAndRemoves is why the call exists: the
// daemon owns every edit to a `.oberth.yaml`, so a client never has to write the
// file itself to add a service or rename one.
func TestGroupsConfigSetAddsRenamesAndRemoves(t *testing.T) {
	fixture := newConfigCase(t)
	c, path := fixture.client, fixture.path

	var snap state.Snapshot
	if e := c.call("state.subscribe", rpc.StateSubscribeParams{}, &snap); e != nil {
		t.Fatalf("state.subscribe: %v", e)
	}

	// Add. The delta collected here is the one published before the reply was
	// queued: the caller cannot see the acknowledgement
	// before the change.
	var res rpc.GroupsConfigSetResult
	seen := callCollectingGroups(t, c, "groups.config.set", rpc.GroupsConfigSetParams{
		Path: path,
		Add: []groups.ServiceAdd{{
			Name: "worker", Port: 9000, Cmd: "uv run worker", Color: "green", DependsOn: []string{"db"},
		}},
	}, &res)
	if !res.OK || len(res.Affected) != 1 || res.Affected[0] != "worker" {
		t.Errorf("result = %+v", res.MutationResult)
	}
	if len(res.Config.Services) != 3 || res.Config.Services[2].Name != "worker" {
		t.Fatalf("returned config = %+v", res.Config.Services)
	}
	g, ok := seen["demo"]
	if !ok {
		t.Fatal("the add was acknowledged before the delta that carries it")
	}
	if len(g.Services) != 3 || g.Services[2].Name != "worker" {
		t.Errorf("the published group is stale: %+v", g.Services)
	}
	raw := readFixture(t, path)
	if !strings.Contains(raw, "# the database goes first") {
		t.Errorf("the comment was lost:\n%s", raw)
	}

	// Rename, and every depends_on reference with it.
	seen = callCollectingGroups(t, c, "groups.config.set", rpc.GroupsConfigSetParams{
		Path:   path,
		Rename: []groups.ServiceRename{{From: "db", To: "database"}},
	}, &res)
	if len(res.Affected) != 1 || res.Affected[0] != "database" {
		t.Errorf("affected = %v, want the new name", res.Affected)
	}
	raw = readFixture(t, path)
	if strings.Contains(raw, "depends_on: [db]") || !strings.Contains(raw, "depends_on: [database]") {
		t.Errorf("the rename did not follow depends_on:\n%s", raw)
	}
	if g, ok := seen["demo"]; ok && g.Services[0].Name != "database" {
		t.Errorf("the published group is stale: %+v", g.Services[0])
	}

	// Remove, and the dependency on it.
	if e := c.call("groups.config.set", rpc.GroupsConfigSetParams{
		Path:   path,
		Remove: []string{"database"},
	}, &res); e != nil {
		t.Fatalf("groups.config.set remove: %v", e)
	}
	if len(res.Affected) != 1 || res.Affected[0] != "database" {
		t.Errorf("affected = %v", res.Affected)
	}
	raw = readFixture(t, path)
	if strings.Contains(raw, "database") {
		t.Errorf("the removed service is still referenced:\n%s", raw)
	}
	if len(res.Config.Services) != 2 {
		t.Errorf("config = %+v, want api and worker left", res.Config.Services)
	}
}

// TestGroupsConfigSetIsAtomic: the four lists are one change. A rename that
// clashes after an add has already been applied must leave the file byte for
// byte as it was.
func TestGroupsConfigSetIsAtomic(t *testing.T) {
	fixture := newConfigCase(t)
	c, path := fixture.client, fixture.path
	before := readFixture(t, path)

	e := c.call("groups.config.set", rpc.GroupsConfigSetParams{
		Path:     path,
		Add:      []groups.ServiceAdd{{Name: "worker", Port: 9000}},
		Rename:   []groups.ServiceRename{{From: "db", To: "api"}},
		Services: []groups.ServiceEdit{{Name: "api", Patch: groups.ServicePatch{}.SetString(groups.FieldIcon, "gear")}},
	}, nil)
	if e == nil {
		t.Fatal("a rename onto an existing name should fail the whole call")
	}
	if e.Code != rpc.CodeConflict || e.Data.Code != "conflict" {
		t.Errorf("error = %+v, want 1011 conflict", e)
	}

	after := readFixture(t, path)
	if after != before {
		t.Errorf("a failed edit changed the file:\n%s", after)
	}
}

// TestGroupsConfigSetEditErrors walks the refusals the edit lists can produce.
// Every one of them must name a code a client can branch on, carry a hint, and
// leave the file untouched — a rejected edit that still writes is worse than an
// accepted one that fails later.
func TestGroupsConfigSetEditErrors(t *testing.T) {
	fixture := newConfigCase(t)
	c, path := fixture.client, fixture.path
	before := readFixture(t, path)

	cases := []struct {
		label string
		edit  rpc.GroupsConfigSetParams
		want  string
	}{
		{"a name the file already has", rpc.GroupsConfigSetParams{
			Path: path, Add: []groups.ServiceAdd{{Name: "api", Port: 9000}},
		}, "conflict"},
		{"a port the file already has", rpc.GroupsConfigSetParams{
			Path: path, Add: []groups.ServiceAdd{{Name: "worker", Port: 8000}},
		}, "conflict"},
		{"renaming a service that is not there", rpc.GroupsConfigSetParams{
			Path: path, Rename: []groups.ServiceRename{{From: "worker", To: "jobs"}},
		}, "not_found"},
		{"removing a service that is not there", rpc.GroupsConfigSetParams{
			Path: path, Remove: []string{"worker"},
		}, "not_found"},
		{"an edit that asks for nothing", rpc.GroupsConfigSetParams{Path: path}, "invalid_params"},
		{"a rename with no target name", rpc.GroupsConfigSetParams{
			Path: path, Rename: []groups.ServiceRename{{From: "db", To: "  "}},
		}, "invalid_params"},
		{"an add with no name", rpc.GroupsConfigSetParams{
			Path: path, Add: []groups.ServiceAdd{{Port: 9000}},
		}, "invalid_params"},
		{"an add whose depends_on names nothing", rpc.GroupsConfigSetParams{
			Path: path, Add: []groups.ServiceAdd{{Name: "worker", Port: 9000, DependsOn: []string{"nope"}}},
		}, "invalid_config"},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			refusal := c.call("groups.config.set", tc.edit, nil)
			if refusal == nil {
				t.Fatalf("the edit was accepted, want %s", tc.want)
			}
			if refusal.Data.Code != tc.want {
				t.Fatalf("code = %q, want %q (%+v)", refusal.Data.Code, tc.want, refusal)
			}
			if refusal.Data.Hint == "" {
				t.Errorf("the refusal carries no hint: %+v", refusal)
			}
		})
	}

	if after := readFixture(t, path); after != before {
		t.Errorf("a refused edit changed the file:\n%s", after)
	}
}

// TestGroupsConfigSetRemoveThenAddReusesThePort: the lists are applied remove
// first, so swapping one service for another on the same port is one call.
func TestGroupsConfigSetRemoveThenAddReusesThePort(t *testing.T) {
	fixture := newConfigCase(t)
	c, path := fixture.client, fixture.path

	var res rpc.GroupsConfigSetResult
	if e := c.call("groups.config.set", rpc.GroupsConfigSetParams{
		Path:   path,
		Remove: []string{"api"},
		Add:    []groups.ServiceAdd{{Name: "gateway", Port: 8000}},
	}, &res); e != nil {
		t.Fatalf("groups.config.set: %v", e)
	}
	if len(res.Affected) != 2 || res.Affected[0] != "api" || res.Affected[1] != "gateway" {
		t.Errorf("affected = %v, want the removal then the add", res.Affected)
	}
	if len(res.Config.Services) != 2 || res.Config.Services[1].Name != "gateway" {
		t.Errorf("config = %+v", res.Config.Services)
	}
}
