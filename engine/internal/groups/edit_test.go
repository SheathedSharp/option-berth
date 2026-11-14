package groups

import (
	"errors"
	"strings"
	"testing"
)

// untidyManifest is the starting file for the add/rename/remove round trips:
// comments above, beside and inside the services list, root keys in an order
// nobody would choose, and one service whose own keys are back to front.
const untidyManifest = `# option-berth's own checkout.
ports: [9230] # debug socket
services:
  # prime the database first.
  - port: 5433
    name: db
    cmd: docker compose up db
  - name: api
    cmd: uv run api # uvicorn
    cwd: backend
    port: 8001
    health: /health
    depends_on: [db, cache]
  # Optional, but the cache makes everything faster.
  - name: cache
    port: 6379
name: option-berth
`

var manifestComments = []string{
	"# option-berth's own checkout.",
	"# debug socket",
	"# prime the database first.",
	"# uvicorn",
	"# Optional, but the cache makes everything faster.",
}

func requireAllComments(t *testing.T, out string) {
	t.Helper()
	for _, want := range manifestComments {
		if !strings.Contains(out, want) {
			t.Errorf("comment %q was lost:\n%s", want, out)
		}
	}
}

// rootKeys lists the mapping keys at column zero, in file order.
func rootKeys(out string) []string {
	var keys []string
	for _, line := range strings.Split(out, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '#' || line[0] == '-' {
			continue
		}
		if colon := strings.Index(line, ":"); colon > 0 {
			keys = append(keys, line[:colon])
		}
	}
	return keys
}

// serviceChunk returns one service's lines, found by its name key wherever in
// the entry it sits. patch_test's serviceLines only finds a service whose first
// key is `name`; here the point is that it need not be.
func serviceChunk(t *testing.T, yaml, name string) []string {
	t.Helper()
	var chunks [][]string
	var current []string
	for _, line := range strings.Split(yaml, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "- "):
			if current != nil {
				chunks = append(chunks, current)
			}
			current = []string{strings.TrimPrefix(trimmed, "- ")}
		case current != nil && strings.HasPrefix(line, "    ") && trimmed != "":
			current = append(current, trimmed)
		case current != nil:
			chunks = append(chunks, current)
			current = nil
		}
	}
	if current != nil {
		chunks = append(chunks, current)
	}
	for _, chunk := range chunks {
		for _, line := range chunk {
			if line == "name: "+name {
				return chunk
			}
		}
	}
	t.Fatalf("no service %q in:\n%s", name, yaml)
	return nil
}

func mustEdit(t *testing.T, path string, edit ConfigEdit) *Config {
	t.Helper()
	cfg, err := EditServices(path, edit)
	if err != nil {
		t.Fatalf("EditServices: %v", err)
	}
	return cfg
}

func namesInFile(t *testing.T, path string) []string {
	t.Helper()
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	names := make([]string, 0, len(cfg.Services))
	for _, service := range cfg.Services {
		names = append(names, service.Name)
	}
	return names
}

// An appended service must leave every other line where the author put it.
func TestAddServiceKeepsCommentsAndOrder(t *testing.T) {
	path := writeManifest(t, untidyManifest)

	cfg := mustEdit(t, path, ConfigEdit{Add: []ServiceAdd{{
		// A service with every editable field filled.
		Name:   "worker",
		Port:   9000,
		Cmd:    "uv run worker",
		Health: "/live",
		// Metadata the client may edit.
		Description: "background jobs",
		Icon:        "gear",
		Color:       "#7c3aed",

		// The dependency is by service name.
		DependsOn: []string{"db"},
	}}})
	out := readFile(t, path)
	requireAllComments(t, out)

	if got := rootKeys(out); !sameStrings(got, []string{"ports", "services", "name"}) {
		t.Errorf("root keys = %v, want the file's own order:\n%s", got, out)
	}
	if got := keyNames(serviceChunk(t, out, "db")); !sameStrings(got, []string{"port", "name", "cmd"}) {
		t.Errorf("db keys = %v, want the file's own order", got)
	}
	if got := namesInFile(t, path); !sameStrings(got, []string{"db", "api", "cache", "worker"}) {
		t.Errorf("services = %v, want worker last", got)
	}
	wantKeys := []string{"name", "cmd", "port", "health", "description", "icon", "color", "depends_on"}
	if got := keyNames(serviceChunk(t, out, "worker")); !sameStrings(got, wantKeys) {
		t.Errorf("worker keys = %v, want %v:\n%s", got, wantKeys, out)
	}
	if !strings.Contains(out, "depends_on: [db]") {
		t.Errorf("depends_on should be flow style:\n%s", out)
	}

	worker := cfg.Services[3]
	if worker.Name != "worker" || worker.Port != 9000 ||
		worker.Color != "#7c3aed" || len(worker.DependsOn) != 1 {
		t.Errorf("returned service = %+v", worker)
	}
}

func TestAddServiceConflicts(t *testing.T) {
	path := writeManifest(t, untidyManifest)
	before := readFile(t, path)

	_, err := EditServices(path, ConfigEdit{Add: []ServiceAdd{{Name: "api", Port: 9000}}})
	var clash *ServiceConflictError
	if !errors.As(err, &clash) {
		t.Fatalf("duplicate name = %v, want *ServiceConflictError", err)
	}
	if clash.Name != "api" || clash.Port != 0 {
		t.Errorf("conflict = %+v, want name api", clash)
	}

	_, err = EditServices(path, ConfigEdit{Add: []ServiceAdd{{Name: "worker", Port: 8001}}})
	if !errors.As(err, &clash) {
		t.Fatalf("duplicate port = %v, want *ServiceConflictError", err)
	}
	if clash.Port != 8001 || clash.Name != "api" {
		t.Errorf("conflict = %+v, want port 8001 held by api", clash)
	}

	_, err = EditServices(path, ConfigEdit{Add: []ServiceAdd{
		// The same name twice in one batch.
		{Name: "worker", Port: 9000},
		{Name: "worker", Port: 9001},
	}})
	if !errors.As(err, &clash) {
		t.Fatalf("same name twice = %v, want *ServiceConflictError", err)
	}
	if after := readFile(t, path); after != before {
		t.Errorf("a refused add changed the file:\n%s", after)
	}
}

func TestAddServiceCreatesTheServicesKey(t *testing.T) {
	path := writeManifest(t, "# just a name\nname: option-berth\nports: [9229]\n")

	if _, err := EditServices(path, ConfigEdit{Add: []ServiceAdd{{Name: "api", Port: 8000}}}); err != nil {
		t.Fatalf("EditServices: %v", err)
	}
	out := readFile(t, path)
	if !strings.Contains(out, "# just a name") {
		t.Errorf("the comment was lost:\n%s", out)
	}
	if got := rootKeys(out); !sameStrings(got, []string{"name", "services", "ports"}) {
		t.Errorf("root keys = %v, want services between name and ports:\n%s", got, out)
	}
}

// A rename that missed a depends_on would leave an invalid file behind, which
// is the whole reason this runs in the runtime.service.
func TestRenameServiceRewritesEveryReference(t *testing.T) {
	path := writeManifest(t, untidyManifest)

	mustEdit(t, path, ConfigEdit{Rename: []ServiceRename{{From: "db", To: "database"}}})
	out := readFile(t, path)
	requireAllComments(t, out)

	if got := namesInFile(t, path); !sameStrings(got, []string{"database", "api", "cache"}) {
		t.Errorf("services = %v, want db renamed in place", got)
	}
	if !strings.Contains(out, "depends_on: [database, cache]") {
		t.Errorf("the depends_on reference was not rewritten:\n%s", out)
	}
	if got := keyNames(serviceChunk(t, out, "database")); !sameStrings(got, []string{"port", "name", "cmd"}) {
		t.Errorf("database keys = %v, want the file's own order", got)
	}
}

func TestRenameServiceErrors(t *testing.T) {
	path := writeManifest(t, untidyManifest)
	before := readFile(t, path)

	_, err := EditServices(path, ConfigEdit{Rename: []ServiceRename{{From: "worker", To: "jobs"}}})
	var missing *ServiceNotFoundError
	if !errors.As(err, &missing) || missing.Name != "worker" {
		t.Fatalf("unknown service = %v, want *ServiceNotFoundError", err)
	}

	_, err = EditServices(path, ConfigEdit{Rename: []ServiceRename{{From: "db", To: "api"}}})
	var clash *ServiceConflictError
	if !errors.As(err, &clash) || clash.Name != "api" {
		t.Fatalf("existing name = %v, want *ServiceConflictError", err)
	}
	if after := readFile(t, path); after != before {
		t.Errorf("a refused rename changed the file:\n%s", after)
	}
}

func TestRenameToItselfIsAllowed(t *testing.T) {
	path := writeManifest(t, untidyManifest)
	mustEdit(t, path, ConfigEdit{Rename: []ServiceRename{{From: "db", To: "db"}}})
}

func TestRemoveServiceDropsDependsOn(t *testing.T) {
	path := writeManifest(t, untidyManifest)

	mustEdit(t, path, ConfigEdit{Remove: []string{"db"}})
	out := readFile(t, path)
	if got := namesInFile(t, path); !sameStrings(got, []string{"api", "cache"}) {
		t.Errorf("services = %v, want db gone", got)
	}
	if !strings.Contains(out, "depends_on: [cache]") {
		t.Errorf("db was not dropped from depends_on:\n%s", out)
	}
	for _, want := range []string{
		"# option-berth's own checkout.",
		"# uvicorn",
		"# Optional, but the cache makes everything faster.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("comment %q was lost:\n%s", want, out)
		}
	}
	if strings.Contains(out, "# prime the database first.") {
		t.Errorf("the removed service's comment should go with it:\n%s", out)
	}

	// Removing the last dependency takes the empty key with it.
	mustEdit(t, path, ConfigEdit{Remove: []string{"cache"}})
	if out := readFile(t, path); strings.Contains(out, "depends_on") {
		t.Errorf("an emptied depends_on should be removed:\n%s", out)
	}
}

func TestRemoveServiceUnknown(t *testing.T) {
	path := writeManifest(t, untidyManifest)
	before := readFile(t, path)

	var missing *ServiceNotFoundError
	if _, err := EditServices(path, ConfigEdit{Remove: []string{"worker"}}); !errors.As(err, &missing) {
		t.Fatalf("unknown service = %v, want *ServiceNotFoundError", err)
	}
	if after := readFile(t, path); after != before {
		t.Errorf("a refused remove changed the file:\n%s", after)
	}
}

// The four lists are one change: a later clash must undo the earlier ones and
// leave the disk untouched.
func TestEditIsAtomic(t *testing.T) {
	path := writeManifest(t, untidyManifest)
	before := readFile(t, path)

	_, err := EditServices(path, ConfigEdit{
		Services: []ServiceEdit{{Name: "cache", Patch: ServicePatch{}.SetString(FieldIcon, "zap")}},
		Rename:   []ServiceRename{{From: "db", To: "api"}},
		Add:      []ServiceAdd{{Name: "worker", Port: 9000}},
	})
	var clash *ServiceConflictError
	if !errors.As(err, &clash) {
		t.Fatalf("the clashing rename should fail the edit, got %v", err)
	}
	if after := readFile(t, path); after != before {
		t.Errorf("a failed edit wrote to the file:\n%s", after)
	}
}

// Remove runs first so swapping a service for another on the same port is one
// call, not two.
func TestRemoveThenAddReusesThePort(t *testing.T) {
	path := writeManifest(t, untidyManifest)

	cfg := mustEdit(t, path, ConfigEdit{
		Remove: []string{"cache"},
		Add:    []ServiceAdd{{Name: "redis", Port: 6379}},
	})
	if got := namesInFile(t, path); !sameStrings(got, []string{"db", "api", "redis"}) {
		t.Errorf("services = %v, want cache replaced by redis", got)
	}
	if cfg.Services[2].Port != 6379 {
		t.Errorf("redis = %+v, want port 6379", cfg.Services[2])
	}
}

func TestEditCombinesEveryList(t *testing.T) {
	path := writeManifest(t, untidyManifest)

	cfg := mustEdit(t, path, ConfigEdit{
		Services: []ServiceEdit{
			{Name: "worker", Patch: ServicePatch{}.SetString(FieldColor, "green")},
		},
		Add:    []ServiceAdd{{Name: "worker", Port: 9000, DependsOn: []string{"database"}}},
		Rename: []ServiceRename{{From: "db", To: "database"}},
		Remove: []string{"cache"},
	})
	if got := namesInFile(t, path); !sameStrings(got, []string{"database", "api", "worker"}) {
		t.Errorf("services = %v", got)
	}
	if cfg.Services[2].Color != "green" {
		t.Errorf("the patch missed the service the add created: %+v", cfg.Services[2])
	}
	if got := cfg.Services[2].DependsOn; len(got) != 1 || got[0] != "database" {
		t.Errorf("worker depends_on = %v, want the renamed database", got)
	}
	out := readFile(t, path)
	for _, want := range []string{
		"# option-berth's own checkout.",
		"# debug socket",
		"# uvicorn",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("comment %q was lost:\n%s", want, out)
		}
	}
}

// Validation runs on the rendered bytes, so a depends_on pointing at nothing
// never reaches the disk.
func TestEditRejectsAnInvalidResult(t *testing.T) {
	path := writeManifest(t, untidyManifest)
	before := readFile(t, path)

	_, err := EditServices(path, ConfigEdit{Add: []ServiceAdd{{
		Name: "worker", Port: 9000, DependsOn: []string{"missing"},
	}}})
	var bad *ConfigError
	if !errors.As(err, &bad) {
		t.Fatalf("dangling depends_on = %v, want *ConfigError", err)
	}
	if after := readFile(t, path); after != before {
		t.Errorf("an invalid edit was written:\n%s", after)
	}
}

func TestRenderEditDoesNotWrite(t *testing.T) {
	path := writeManifest(t, untidyManifest)
	before := readFile(t, path)

	data, cfg, err := RenderEdit(path, ConfigEdit{Add: []ServiceAdd{{Name: "worker", Port: 9000}}})
	if err != nil {
		t.Fatalf("RenderEdit: %v", err)
	}
	if !strings.Contains(string(data), "name: worker") {
		t.Errorf("the bytes do not carry the add:\n%s", data)
	}
	if len(cfg.Services) != 4 {
		t.Errorf("rendered config has %d services, want 4", len(cfg.Services))
	}
	if after := readFile(t, path); after != before {
		t.Errorf("RenderEdit wrote to the file:\n%s", after)
	}
}

func TestConfigEditAffectedAndEmpty(t *testing.T) {
	if !(ConfigEdit{}).Empty() {
		t.Error("an edit with no lists is not empty")
	}
	edit := ConfigEdit{
		Services: []ServiceEdit{{Name: "api"}},
		Add:      []ServiceAdd{{Name: "worker"}},
		Rename:   []ServiceRename{{From: "db", To: "database"}},
		Remove:   []string{"old"},
	}
	if edit.Empty() {
		t.Error("an edit with four lists reads as empty")
	}
	want := []string{"old", "database", "worker", "api"}
	if got := edit.Affected(); !sameStrings(got, want) {
		t.Errorf("Affected() = %v, want %v", got, want)
	}
}

// A client that edits names, ports and colours must not throw away the command
// the proposal guessed.
func TestCurateKeepsTheProposedCommand(t *testing.T) {
	proposal := &Config{
		Name: "demo",
		// The proposal found these two services.
		Services: []Service{
			{Name: "node-8000", Port: 8000, Cmd: "npm run dev", Cwd: "web"},
			{Name: "postgres-5432", Port: 5432, Cmd: "docker compose up db"},
		},
	}
	curated := Curate(proposal, []ServiceAdd{
		{Name: "web", Port: 8000, Color: "blue"},
		{Name: "db", Port: 5432, Cmd: "docker start db"},
		{Name: "worker", Port: 9000},
	})

	if len(curated.Services) != 3 || curated.Name != "demo" {
		t.Fatalf("curated config = %+v", curated)
	}
	web := curated.Services[0]
	if web.Cmd != "npm run dev" || web.Cwd != "web" || web.Color != "blue" {
		t.Errorf("web = %+v, want the proposal's cmd and cwd", web)
	}
	if curated.Services[1].Cmd != "docker start db" {
		t.Errorf("db = %+v, want the caller's cmd", curated.Services[1])
	}
	if curated.Services[2].Cmd != "" {
		t.Errorf("worker = %+v, want no cmd: its port was not proposed", curated.Services[2])
	}
	if proposal.Services[0].Name != "node-8000" {
		t.Errorf("Curate mutated the proposal: %+v", proposal.Services[0])
	}
}

func TestAddsFromRoundTrips(t *testing.T) {
	cfg := &Config{Services: []Service{{
		Name: "api", Prepare: "mvn package", Port: 8000, Cmd: "uv run api", Cwd: "backend",
		Health: "/health", Description: "the api", Icon: "server", Color: "blue",
		DependsOn: []string{"db"},
	}}}
	adds := AddsFrom(cfg)
	if len(adds) != 1 {
		t.Fatalf("AddsFrom returned %d entries", len(adds))
	}
	back := adds[0].Service()
	if back.Name != "api" || back.Prepare != "mvn package" || back.Port != 8000 ||
		back.Cmd != "uv run api" || back.Cwd != "backend" || back.Health != "/health" ||
		back.Description != "the api" || back.Icon != "server" || back.Color != "blue" ||
		len(back.DependsOn) != 1 {
		t.Errorf("round trip = %+v", back)
	}
}

// A file with no worktree_ports grows the key at the end; every other line
// stays where it was.
func TestWorktreePortsAddKeepsCommentsAndOrder(t *testing.T) {
	path := writeManifest(t, untidyManifest)

	cfg := mustEdit(t, path, ConfigEdit{WorktreePorts: SetInt(4)})
	if cfg.WorktreePorts == nil || *cfg.WorktreePorts != 4 {
		t.Fatalf("returned worktree_ports = %v, want 4", cfg.WorktreePorts)
	}
	out := readFile(t, path)
	requireAllComments(t, out)
	if got := strings.Join(rootKeys(out), ","); got != "ports,services,name,worktree_ports" {
		t.Errorf("root keys = %s, want worktree_ports last:\n%s", got, out)
	}
	if got := namesInFile(t, path); strings.Join(got, ",") != "db,api,cache" {
		t.Errorf("services = %v, want untouched", got)
	}
	reloaded, err := Load(path)
	if err != nil || reloaded.WorktreePorts == nil || *reloaded.WorktreePorts != 4 {
		t.Fatalf("reloaded worktree_ports = %v, %v", reloaded, err)
	}
}

// An existing value is edited in place, keeping its comment and position; a
// clear removes the key and nothing else.
func TestWorktreePortsReplaceAndRemove(t *testing.T) {
	path := writeManifest(t, `name: shop
worktree_ports: 3 # api, web, worker
# services below
services:
  - name: api
    port: 8010
`)

	mustEdit(t, path, ConfigEdit{WorktreePorts: SetInt(6)})
	out := readFile(t, path)
	if !strings.Contains(out, "worktree_ports: 6 # api, web, worker") {
		t.Errorf("the line comment did not stay:\n%s", out)
	}
	if got := strings.Join(rootKeys(out), ","); got != "name,worktree_ports,services" {
		t.Errorf("root keys = %s, want the key where it was", got)
	}

	cfg := mustEdit(t, path, ConfigEdit{WorktreePorts: ClearInt()})
	if cfg.WorktreePorts != nil {
		t.Errorf("worktree_ports = %d after clear", *cfg.WorktreePorts)
	}
	out = readFile(t, path)
	if strings.Contains(out, "worktree_ports") {
		t.Errorf("the key survived a clear:\n%s", out)
	}
	if !strings.Contains(out, "# services below") || !strings.Contains(out, "name: api") {
		t.Errorf("a clear touched more than its key:\n%s", out)
	}

	mustEdit(t, path, ConfigEdit{WorktreePorts: ClearInt()})
}

func TestWorktreePortsOutOfRangeLeavesTheFile(t *testing.T) {
	path := writeManifest(t, untidyManifest)
	for _, value := range []int{0, MaxWorktreePorts + 1} {
		_, err := EditServices(path, ConfigEdit{WorktreePorts: SetInt(value)})
		var bad *ConfigError
		if !errors.As(err, &bad) {
			t.Fatalf("worktree_ports %d: err = %v, want *ConfigError", value, err)
		}
		if got := readFile(t, path); got != untidyManifest {
			t.Fatalf("worktree_ports %d changed the file:\n%s", value, got)
		}
	}
}

func TestWorktreePortsChangeIsNotEmpty(t *testing.T) {
	for _, change := range []IntChange{SetInt(2), ClearInt()} {
		edit := ConfigEdit{WorktreePorts: change}
		if edit.Empty() {
			t.Errorf("%+v reads as empty", change)
		}
		if len(edit.Affected()) != 0 {
			t.Errorf("affected = %v, want no service names", edit.Affected())
		}
	}
	if !(ConfigEdit{}).Empty() {
		t.Error("the zero edit is not empty")
	}
}
