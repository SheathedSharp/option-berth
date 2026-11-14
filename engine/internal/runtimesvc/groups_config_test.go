package runtimesvc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// demoManifest is the project the config handlers are exercised against: one
// service that is running (db) and one that is only declared (api, which
// depends on db).
const demoManifest = `# the demo project
name: demo
services:
  # declared first: everything else depends on it
  - name: db
    port: 5432
    cmd: postgres
  - name: api
    health: /health
    cmd: uv run api
    port: 8000
    depends_on: [db]
`

// resolvedDir returns dir with its symlinks resolved, which is the form the
// scanner reports: t.TempDir can sit behind /var → /private/var on macOS, and
// comparing against the raw form would fail for reasons that have nothing to
// do with the handler under test.
func resolvedDir(t *testing.T, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return dir
	}
	return real
}

// configRig is one runtime service, a client bound to it and the manifest file it was
// pointed at. Cancelling the test's context shuts the runtime service down and the
// constructors arrange that themselves, so a test body starts at the call it
// means to make.
type configRig struct {
	t    *testing.T
	h    *testHarness
	cl   *testClient
	dir  string
	file string
}

// newConfigRig builds a runtime service whose index already holds demoManifest, seen the
// way the scanner sees one: through a listening process's cwd.
func newConfigRig(t *testing.T) *configRig {
	t.Helper()
	// The directory is claimed before the harness, so the harness stops before
	// the directory it indexed is removed.
	dir := resolvedDir(t, t.TempDir())
	file := filepath.Join(dir, groups.ConfigName)
	if err := os.WriteFile(file, []byte(demoManifest), 0o644); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, t.Context())
	h.setRows(ports.ListeningPort{
		Port: 5432, BindAddress: "127.0.0.1", PID: 501, Process: "postgres", Cwd: dir,
	})
	if _, err := h.loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatalf("priming the scanner: %v", err)
	}
	return &configRig{t: t, h: h, cl: h.dial(t.Context()), dir: dir, file: file}
}

// newBareRig is newConfigRig without the fixture manifest: the test writes its
// own file, so the runtime service has never scanned the directory.
func newBareRig(t *testing.T) *configRig {
	t.Helper()
	dir := resolvedDir(t, t.TempDir())
	h := newHarness(t, t.Context())
	return &configRig{
		t:    t,
		h:    h,
		cl:   h.dial(t.Context()),
		dir:  dir,
		file: filepath.Join(dir, groups.ConfigName),
	}
}

// manifest returns the file's bytes as they are on disk right now, ending the
// test when it cannot be read: several cases compare the file before and after
// a handler, and a read failure there is always a broken fixture rather than a
// behaviour under test.
func (r *configRig) manifest() string {
	r.t.Helper()
	body, err := os.ReadFile(r.file)
	if err != nil {
		r.t.Fatalf("reading %s: %v", r.file, err)
	}
	return string(body)
}

// requireManifest fails unless the manifest on disk contains want. The hint
// says what losing it would mean, so a failure names the consequence rather
// than just the missing substring.
func (r *configRig) requireManifest(want, hint string) {
	r.t.Helper()
	if body := r.manifest(); !strings.Contains(body, want) {
		r.t.Fatalf("%s (%q missing):\n%s", hint, want, body)
	}
}

// TestGroupsConfigGetByName returns the file as the resolver publishes it,
// joined against what is actually running.
func TestGroupsConfigGetByName(t *testing.T) {
	rig := newConfigRig(t)

	name := "demo"
	var res rpc.GroupsConfigGetResult
	if e := rig.cl.call("groups.config.get", rpc.GroupsConfigGetParams{Name: &name}, &res); e != nil {
		t.Fatalf("groups.config.get: %v", e)
	}
	if res.Path != rig.file {
		t.Errorf("path = %q, want %q", res.Path, rig.file)
	}
	if res.Config.Name != "demo" || len(res.Config.Services) != 2 {
		t.Fatalf("config = %+v, want demo with its two services", res.Config)
	}
	db, api := res.Config.Services[0], res.Config.Services[1]
	if db.Name != "db" || !db.Running {
		t.Errorf("db row = %+v, want it running", db)
	}
	if db.PortActual == nil || *db.PortActual != 5432 {
		t.Errorf("db port_actual = %v, want the 5432 that is listening", db.PortActual)
	}
	if api.Health == nil || *api.Health != "/health" {
		t.Errorf("api health = %v, want the declared /health", api.Health)
	}
	if api.Running {
		t.Errorf("api row = %+v, want it not running", api)
	}
	if api.Description != nil || api.Icon != nil || api.Color != nil {
		t.Errorf("api metadata should be null: %+v", api)
	}
}

// TestGroupsConfigGetByPath also indexes a manifest the runtime service has never
// scanned.
func TestGroupsConfigGetByPath(t *testing.T) {
	rig := newBareRig(t)
	if err := os.WriteFile(rig.file, []byte(demoManifest), 0o644); err != nil {
		t.Fatal(err)
	}

	path := rig.file
	var res rpc.GroupsConfigGetResult
	if e := rig.cl.call("groups.config.get", rpc.GroupsConfigGetParams{Path: &path}, &res); e != nil {
		t.Fatalf("groups.config.get by path: %v", e)
	}
	if res.Config.Name != "demo" {
		t.Fatalf("config = %+v, want the file the runtime service never scanned", res.Config)
	}
}

func TestGroupsConfigGetNotFound(t *testing.T) {
	rig := newConfigRig(t)

	name := "nope"
	if e := rig.cl.call("groups.config.get", rpc.GroupsConfigGetParams{Name: &name}, nil); e == nil {
		t.Fatal("an unknown group name resolved")
	} else if e.Data.Code != "not_found" {
		t.Errorf("error = %+v, want not_found", e)
	}

	missing := filepath.Join(rig.dir, "sub", groups.ConfigName)
	if e := rig.cl.call("groups.config.get", rpc.GroupsConfigGetParams{Path: &missing}, nil); e == nil {
		t.Fatal("a missing path resolved")
	} else if e.Data.Code != "not_found" {
		t.Errorf("error = %+v, want not_found", e)
	}

	// Neither name nor path is a caller mistake, not a lookup that failed.
	if e := rig.cl.call("groups.config.get", rpc.GroupsConfigGetParams{}, nil); e == nil {
		t.Fatal("a call with neither name nor path resolved")
	} else if e.Data.Code != "invalid_params" {
		t.Errorf("a call with neither name nor path: error = %+v, want invalid_params", e)
	}
}

// TestGroupsConfigSetWritesAndPublishes is the round trip a desktop editor
// makes: patch one service, keep the comments, and see the change in a delta
// without having to ask for it.
func TestGroupsConfigSetWritesAndPublishes(t *testing.T) {
	rig := newConfigRig(t)

	// Subscribe first, so the delta the write publishes has somewhere to go.
	var snap state.Snapshot
	if e := rig.cl.call("state.subscribe", rpc.StateSubscribeParams{}, &snap); e != nil {
		t.Fatalf("state.subscribe: %v", e)
	}

	// The handler publishes before it replies, so the delta is already on the
	// wire when the response arrives: one loop collects both.
	blueHealth := groups.ServicePatch{}.
		SetString(groups.FieldHealth, "/").
		SetString(groups.FieldColor, "blue")
	var res rpc.GroupsConfigSetResult
	deltas := callGatheringGroups(t, rig.cl, "groups.config.set", rpc.GroupsConfigSetParams{
		Path:     rig.file,
		Services: []groups.ServiceEdit{{Name: "db", Patch: blueHealth}},
	}, &res)
	if !res.OK || len(res.Affected) != 1 || res.Affected[0] != "db" {
		t.Errorf("result = %+v, want the db patch acknowledged", res.MutationResult)
	}
	db := res.Config.Services[0]
	if db.Health == nil || *db.Health != "/" || db.Color == nil || *db.Color != "blue" {
		t.Fatalf("returned row = %+v", db)
	}

	rig.requireManifest("# declared first: everything else depends on it",
		"the write dropped a comment the editor must preserve")
	rig.requireManifest("color: blue", "the patch never reached the file")

	// The delta carries the edited group.
	g, seen := deltas["demo"]
	if !seen {
		published := awaitGroup(t, rig.cl, "demo")
		if published == nil {
			t.Fatal("no groups delta after the write")
		}
		g = *published
	}
	if g.Services[0].Color == nil || *g.Services[0].Color != "blue" {
		t.Errorf("the published group is stale: %+v", g.Services[0])
	}
}

// callGatheringGroups makes one call and returns every group that arrived in a
// delta while it was in flight, keyed by name.
func callGatheringGroups(t *testing.T, cl *testClient, method string, params, out any) map[string]state.Group {
	t.Helper()
	id := cl.send(method, params)
	gathered := map[string]state.Group{}
	for {
		m := cl.read()
		switch {
		case m.Method == rpc.MethodStateDelta:
			var d state.Delta
			if err := json.Unmarshal(m.Params, &d); err != nil {
				t.Fatalf("decoding delta: %v", err)
			}
			for _, g := range append(append([]state.Group{}, d.Groups.Added...), d.Groups.Updated...) {
				gathered[g.Name] = g
			}
		case m.IsResponse() && string(m.ID) == id:
			if m.Error != nil {
				t.Fatalf("%s failed: %v", method, m.Error)
			}
			if out == nil || len(m.Result) == 0 {
				return gathered
			}
			if err := json.Unmarshal(m.Result, out); err != nil {
				t.Fatalf("decoding the %s result: %v", method, err)
			}
			return gathered
		}
	}
}

func TestGroupsConfigSetErrors(t *testing.T) {
	rig := newConfigRig(t)
	before := rig.manifest()

	// A service the file does not declare.
	if e := rig.cl.call("groups.config.set", rpc.GroupsConfigSetParams{
		Path:     rig.file,
		Services: []groups.ServiceEdit{{Name: "worker", Patch: groups.ServicePatch{}.SetString(groups.FieldIcon, "gear")}},
	}, nil); e == nil {
		t.Fatal("patching an unknown service was accepted")
	} else if e.Data.Code != "not_found" {
		t.Errorf("error = %+v, want not_found", e)
	}

	// A patch that would make the file invalid.
	e := rig.cl.call("groups.config.set", rpc.GroupsConfigSetParams{
		Path:     rig.file,
		Services: []groups.ServiceEdit{{Name: "api", Patch: groups.ServicePatch{}.SetPort(70000)}},
	}, nil)
	if e == nil {
		t.Fatal("an invalid port was accepted")
	}
	if e.Code != rpc.CodeInvalidConfig || e.Data.Code != "invalid_config" {
		t.Errorf("error = %+v, want 1006 invalid_config", e)
	}
	if !strings.Contains(e.Data.Detail, "70000") {
		t.Errorf("detail should name the bad value: %q", e.Data.Detail)
	}

	// Anything that is not a oberth.yaml is refused outright.
	if e := rig.cl.call("groups.config.set", rpc.GroupsConfigSetParams{
		Path:     filepath.Join(rig.dir, "docker-compose.yml"),
		Services: []groups.ServiceEdit{{Name: "api", Patch: groups.ServicePatch{}.SetString(groups.FieldIcon, "x")}},
	}, nil); e == nil || e.Data.Code != "invalid_params" {
		t.Errorf("error = %+v, want invalid_params", e)
	}

	if after := rig.manifest(); after != before {
		t.Errorf("a failed write changed the file:\n%s", after)
	}
}

// TestGroupsReloadPicksUpANewFile: the index is long-lived, so this is the
// method that makes a project created after the runtime service started a group.
func TestGroupsReloadPicksUpANewFile(t *testing.T) {
	rig := newConfigRig(t)

	sub := filepath.Join(rig.dir, "nested")
	nested := filepath.Join(sub, groups.ConfigName)
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested,
		[]byte("name: nested\nservices:\n  - name: worker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The runtime service only knows the parent directory, so nothing has seen this file
	// yet; the reload has to walk the roots it remembers.
	rig.h.loop.ObserveConfig(sub)

	var res rpc.GroupsReloadResult
	if e := rig.cl.call("groups.reload", rpc.Empty{}, &res); e != nil {
		t.Fatalf("groups.reload: %v", e)
	}
	if res.Loaded != 2 || len(res.Errors) != 0 {
		t.Fatalf("reload = %+v, want both files loaded and no errors", res)
	}

	name := "nested"
	var got rpc.GroupsConfigGetResult
	if e := rig.cl.call("groups.config.get", rpc.GroupsConfigGetParams{Name: &name}, &got); e != nil {
		t.Fatalf("the new config is not in the index: %v", e)
	}
	if got.Config.Services[0].Name != "worker" {
		t.Errorf("config = %+v, want the nested project's worker", got.Config)
	}
}

// TestGroupsReloadReportsBrokenFiles: a file that does not validate is listed,
// never fatal.
func TestGroupsReloadReportsBrokenFiles(t *testing.T) {
	rig := newConfigRig(t)

	if err := os.WriteFile(rig.file, []byte("name: two words\nservices:\n  - name: api\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var res rpc.GroupsReloadResult
	if e := rig.cl.call("groups.reload", rpc.Empty{}, &res); e != nil {
		t.Fatalf("groups.reload: %v", e)
	}
	if res.Loaded != 0 || len(res.Errors) != 1 {
		t.Fatalf("reload = %+v, want the one broken file reported", res)
	}
	if res.Errors[0].Path != rig.file || !strings.Contains(res.Errors[0].Error, "whitespace") {
		t.Errorf("error row = %+v", res.Errors[0])
	}
}

// awaitGroup reads notifications until a delta carries the named group.
func awaitGroup(t *testing.T, cl *testClient, name string) *state.Group {
	t.Helper()
	for range 20 {
		msg := cl.read()
		if msg.Method != rpc.MethodStateDelta {
			continue
		}
		var delta state.Delta
		if err := json.Unmarshal(msg.Params, &delta); err != nil {
			t.Fatalf("decoding the delta: %v", err)
		}
		for _, g := range append(append([]state.Group{}, delta.Groups.Added...), delta.Groups.Updated...) {
			if g.Name == name {
				return &g
			}
		}
	}
	return nil
}

// TestGroupsConfigSetAddsRenamesAndRemoves is why the call exists: the runtime service
// owns every edit to an oberth.yaml, so a client never has to write the file
// itself to add a service or rename one.
func TestGroupsConfigSetAddsRenamesAndRemoves(t *testing.T) {
	rig := newConfigRig(t)

	var snap state.Snapshot
	if e := rig.cl.call("state.subscribe", rpc.StateSubscribeParams{}, &snap); e != nil {
		t.Fatalf("state.subscribe: %v", e)
	}

	// Add. The delta collected here was published before the reply was queued:
	// the caller cannot see the acknowledgement before the change.
	worker := groups.ServiceAdd{
		Name: "worker", Port: 9000, Cmd: "uv run worker", Color: "green", DependsOn: []string{"db"},
	}
	var res rpc.GroupsConfigSetResult
	seen := callGatheringGroups(t, rig.cl, "groups.config.set", rpc.GroupsConfigSetParams{
		Path: rig.file,
		Add:  []groups.ServiceAdd{worker},
	}, &res)
	if !res.OK || len(res.Affected) != 1 || res.Affected[0] != "worker" {
		t.Errorf("result = %+v, want the add acknowledged", res.MutationResult)
	}
	if len(res.Config.Services) != 3 || res.Config.Services[2].Name != "worker" {
		t.Fatalf("returned config = %+v", res.Config.Services)
	}
	g, seenDelta := seen["demo"]
	if !seenDelta {
		t.Fatal("the add was acknowledged before the delta that carries it")
	}
	if len(g.Services) != 3 || g.Services[2].Name != "worker" {
		t.Errorf("the published group is stale: %+v", g.Services)
	}
	if raw := rig.manifest(); !strings.Contains(raw, "# declared first: everything else depends on it") {
		t.Errorf("the comment was lost:\n%s", raw)
	}

	// Rename, and every depends_on reference with it.
	seen = callGatheringGroups(t, rig.cl, "groups.config.set", rpc.GroupsConfigSetParams{
		Path:   rig.file,
		Rename: []groups.ServiceRename{{From: "db", To: "database"}},
	}, &res)
	if len(res.Affected) != 1 || res.Affected[0] != "database" {
		t.Errorf("affected = %v, want the new name", res.Affected)
	}
	raw := rig.manifest()
	if strings.Contains(raw, "depends_on: [db]") || !strings.Contains(raw, "depends_on: [database]") {
		t.Errorf("the rename did not follow depends_on:\n%s", raw)
	}
	if g, ok := seen["demo"]; ok && g.Services[0].Name != "database" {
		t.Errorf("the published group is stale: %+v", g.Services[0])
	}

	// Remove, and the dependency on it.
	if e := rig.cl.call("groups.config.set", rpc.GroupsConfigSetParams{
		Path:   rig.file,
		Remove: []string{"database"},
	}, &res); e != nil {
		t.Fatalf("removing database: %v", e)
	}
	if len(res.Affected) != 1 || res.Affected[0] != "database" {
		t.Errorf("affected = %v, want the removed name", res.Affected)
	}
	raw = rig.manifest()
	if strings.Contains(raw, "database") {
		t.Errorf("the removed service is still referenced:\n%s", raw)
	}
	if len(res.Config.Services) != 2 {
		t.Errorf("config = %+v, want api and worker left behind", res.Config.Services)
	}
}

// TestGroupsConfigSetIsAtomic: the four lists are one change, so a rename that
// clashes after an add has already been applied must leave the file byte for
// byte as it was.
func TestGroupsConfigSetIsAtomic(t *testing.T) {
	rig := newConfigRig(t)
	before := rig.manifest()

	clash := rpc.GroupsConfigSetParams{
		Path:     rig.file,
		Add:      []groups.ServiceAdd{{Name: "worker", Port: 9000}},
		Rename:   []groups.ServiceRename{{From: "db", To: "api"}},
		Services: []groups.ServiceEdit{{Name: "api", Patch: groups.ServicePatch{}.SetString(groups.FieldIcon, "gear")}},
	}
	e := rig.cl.call("groups.config.set", clash, nil)
	if e == nil {
		t.Fatal("a rename onto an existing name was accepted")
	}
	if e.Code != rpc.CodeConflict || e.Data.Code != "conflict" {
		t.Errorf("error = %+v, want 1011 conflict", e)
	}

	if after := rig.manifest(); after != before {
		t.Errorf("a failed edit changed the file:\n%s", after)
	}
}

// TestGroupsConfigSetEditErrors walks the refusals the edit lists can produce.
// Every one of them must name a code a client can branch on, carry a hint, and
// leave the file untouched — a rejected edit that still writes is worse than an
// accepted one that fails later.
func TestGroupsConfigSetEditErrors(t *testing.T) {
	rig := newConfigRig(t)
	before := rig.manifest()

	refusals := []struct {
		what string
		edit rpc.GroupsConfigSetParams
		code string
	}{
		{"a name the file already has", rpc.GroupsConfigSetParams{
			Path: rig.file, Add: []groups.ServiceAdd{{Name: "api", Port: 9000}},
		}, "conflict"},
		{"a port the file already has", rpc.GroupsConfigSetParams{
			Path: rig.file, Add: []groups.ServiceAdd{{Name: "worker", Port: 8000}},
		}, "conflict"},
		{"renaming a service that is not there", rpc.GroupsConfigSetParams{
			Path: rig.file, Rename: []groups.ServiceRename{{From: "worker", To: "jobs"}},
		}, "not_found"},
		{"removing a service that is not there", rpc.GroupsConfigSetParams{
			Path: rig.file, Remove: []string{"worker"},
		}, "not_found"},
		{"an edit that asks for nothing", rpc.GroupsConfigSetParams{Path: rig.file}, "invalid_params"},
		{"a rename with no target name", rpc.GroupsConfigSetParams{
			Path: rig.file, Rename: []groups.ServiceRename{{From: "db", To: "  "}},
		}, "invalid_params"},
		{"an add with no name", rpc.GroupsConfigSetParams{
			Path: rig.file, Add: []groups.ServiceAdd{{Port: 9000}},
		}, "invalid_params"},
		{"an add whose depends_on names nothing", rpc.GroupsConfigSetParams{
			Path: rig.file, Add: []groups.ServiceAdd{{Name: "worker", Port: 9000, DependsOn: []string{"nope"}}},
		}, "invalid_config"},
	}

	for _, refusal := range refusals {
		t.Run(refusal.what, func(t *testing.T) {
			e := rig.cl.call("groups.config.set", refusal.edit, nil)
			if e == nil {
				t.Fatalf("the edit was accepted, want %s", refusal.code)
			}
			if e.Data.Code != refusal.code {
				t.Fatalf("code = %q, want %q (%+v)", e.Data.Code, refusal.code, e)
			}
			if e.Data.Hint == "" {
				t.Errorf("the refusal carries no hint: %+v", e)
			}
		})
	}

	if after := rig.manifest(); after != before {
		t.Errorf("a refused edit changed the file:\n%s", after)
	}
}

// TestGroupsConfigSetRemoveThenAddReusesThePort: the lists are applied remove
// first, so swapping one service for another on the same port is one call.
func TestGroupsConfigSetRemoveThenAddReusesThePort(t *testing.T) {
	rig := newConfigRig(t)

	var res rpc.GroupsConfigSetResult
	if e := rig.cl.call("groups.config.set", rpc.GroupsConfigSetParams{
		Path:   rig.file,
		Remove: []string{"api"},
		Add:    []groups.ServiceAdd{{Name: "frontend", Port: 8000}},
	}, &res); e != nil {
		t.Fatalf("swapping api for frontend on the same port: %v", e)
	}
	if len(res.Affected) != 2 || res.Affected[0] != "api" || res.Affected[1] != "frontend" {
		t.Errorf("affected = %v, want the removal then the add", res.Affected)
	}
	if len(res.Config.Services) != 2 || res.Config.Services[1].Name != "frontend" {
		t.Errorf("config = %+v, want frontend in api's place", res.Config.Services)
	}
}
