package daemon

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// initRig is a daemon that can see two services listening inside a git
// checkout that has no manifest yet — the state groups.init exists to get out
// of.
type initRig struct {
	t    *testing.T
	h    *testHarness
	cl   *testClient
	root string // the checkout the proposal is for
	file string // the manifest groups.init would write
}

func newInitRig(t *testing.T) *initRig {
	t.Helper()
	root := resolvedDir(t, t.TempDir())
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, t.Context())
	api := ports.ListeningPort{
		Port: 8000, BindAddress: "127.0.0.1", PID: 4242,
		Process: "python", Display: "api", Command: "uv run api", Cwd: root,
	}
	web := ports.ListeningPort{
		Port: 5173, BindAddress: "127.0.0.1", PID: 4243,
		Process: "node", Display: "web", Command: "npm run dev", Cwd: filepath.Join(root, "web"),
	}
	// Somebody else's process in somebody else's directory: it must not reach
	// this project's file.
	stranger := ports.ListeningPort{
		Port: 9999, BindAddress: "127.0.0.1", PID: 4244,
		Process: "other", Display: "other", Command: "other", Cwd: resolvedDir(t, t.TempDir()),
	}
	h.setRows(api, web, stranger)
	if _, err := h.loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatalf("priming the scanner: %v", err)
	}
	return &initRig{
		t:    t,
		h:    h,
		cl:   h.dial(t.Context()),
		root: root,
		file: filepath.Join(root, groups.ConfigName),
	}
}

// init sends groups.init the way a caller does and hands back the answer.
func (r *initRig) init(p rpc.GroupsInitParams) (rpc.GroupsInitResult, *rpc.Error) {
	r.t.Helper()
	var res rpc.GroupsInitResult
	e := r.cl.call("groups.init", p, &res)
	return res, e
}

// proposal is the default call: a dry run over a checkout with no manifest,
// which must never fail.
func (r *initRig) proposal() rpc.GroupsInitResult {
	r.t.Helper()
	res, e := r.init(rpc.GroupsInitParams{RootDir: r.root})
	if e != nil {
		r.t.Fatalf("groups.init: %v", e)
	}
	return res
}

// onDisk is the manifest as it is on disk right now.
func (r *initRig) onDisk() string {
	r.t.Helper()
	body, err := os.ReadFile(r.file)
	if err != nil {
		r.t.Fatalf("reading %s: %v", r.file, err)
	}
	return string(body)
}

// load writes a proposal to a scratch file and reads it back through the real
// loader, which is the check that matters: the daemon must never propose
// something groups.Load would reject.
func (r *initRig) load(text string) *groups.Config {
	r.t.Helper()
	path := filepath.Join(r.t.TempDir(), groups.ConfigName)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
	cfg, err := groups.Load(path)
	if err != nil {
		r.t.Fatalf("the proposal does not load: %v\n%s", err, text)
	}
	return cfg
}

// declaredPorts is the ports a proposed file declares, in order.
func (r *initRig) declaredPorts(text string) []int {
	r.t.Helper()
	cfg := r.load(text)
	out := make([]int, 0, len(cfg.Services))
	for _, s := range cfg.Services {
		out = append(out, s.Port)
	}
	return out
}

// groupNamed picks one group out of a list by name.
func groupNamed(gg []state.Group, name string) (state.Group, bool) {
	if i := slices.IndexFunc(gg, func(g state.Group) bool { return g.Name == name }); i >= 0 {
		return gg[i], true
	}
	return state.Group{}, false
}

// TestGroupsInitDryRunReturnsAParsableProposal is the default call: no write,
// and what comes back is the exact file that would have been written.
func TestGroupsInitDryRunReturnsAParsableProposal(t *testing.T) {
	rig := newInitRig(t)

	res := rig.proposal()
	if !res.OK || res.Path != rig.file {
		t.Fatalf("result = %+v, want ok with path %q", res, rig.file)
	}
	if _, err := os.Lstat(rig.file); !os.IsNotExist(err) {
		t.Fatalf("a dry run wrote %s (err = %v)", rig.file, err)
	}
	if !strings.Contains(res.YAML, "oberth init") {
		t.Errorf("the yaml carries no generated header:\n%s", res.YAML)
	}

	// The proposal has to survive the loader, or `write: true` would put an
	// unusable file on disk.
	cfg := rig.load(res.YAML)
	if cfg.Name != res.Proposal.Name {
		t.Errorf("the loader reads name %q, the proposal says %q", cfg.Name, res.Proposal.Name)
	}
	// Nothing is declared: two ports are listening in the checkout, and neither
	// becomes a service. option-berth has no way to state how to start either
	// one — the command line a process was launched with is a snapshot of that
	// run.
	if len(cfg.Services) != 0 {
		t.Fatalf("parsed services = %+v, want none: a proposal declares nothing", cfg.Services)
	}
	if got := rig.declaredPorts(res.YAML); len(got) != 0 {
		t.Errorf("declared ports = %v, want none", got)
	}
	if len(res.Affected) != 0 {
		t.Errorf("affected = %v, want none — no service was written", res.Affected)
	}
	// What it saw is still reported, as the context whoever writes the list
	// needs.
	for _, want := range []string{"8000", "5173", "api", "web"} {
		if !strings.Contains(res.YAML, want) {
			t.Errorf("the file does not mention %q as context:\n%s", want, res.YAML)
		}
	}
	if res.Proposal.ConfigPath == nil || *res.Proposal.ConfigPath != rig.file {
		t.Errorf("proposal config_path = %v, want %q", res.Proposal.ConfigPath, rig.file)
	}
	if len(res.Proposal.Services) != 0 || len(res.Proposal.Members) != 0 || res.Proposal.Status != "stopped" {
		t.Errorf("proposal group = %+v, want a file with nothing declared", res.Proposal)
	}
}

// TestGroupsInitCarriesDeclarationsAsComments: what the project's own files
// state (a compose service, a package.json dev script) is read and handed over
// as a comment with its source, and the list stays empty — the same rule the
// CLI's `oberth init` follows, so the two paths write the same file.
func TestGroupsInitCarriesDeclarationsAsComments(t *testing.T) {
	rig := newInitRig(t)

	if err := os.WriteFile(filepath.Join(rig.root, "compose.yaml"),
		[]byte("services:\n  db:\n    image: postgres\n    ports: [\"5432:5432\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rig.root, "package.json"),
		[]byte(`{"scripts":{"dev":"vite"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res := rig.proposal()
	if cfg := rig.load(res.YAML); len(cfg.Services) != 0 {
		t.Fatalf("parsed services = %+v, want none: a declaration is not this project's service", cfg.Services)
	}
	for _, want := range []string{
		"dev  npm run dev  [package.json (scripts.dev)]",
		"docker compose up db  —  port 5432  [compose.yaml]",
	} {
		if !strings.Contains(res.YAML, want) {
			t.Errorf("the file does not carry %q as context:\n%s", want, res.YAML)
		}
	}
}

// TestGroupsInitWriteFlipsTheGroupToFile is the whole point of the method: the
// file lands on disk and the next read already calls the group a file group.
func TestGroupsInitWriteFlipsTheGroupToFile(t *testing.T) {
	rig := newInitRig(t)

	res, e := rig.init(rpc.GroupsInitParams{RootDir: rig.root, Write: true})
	if e != nil {
		t.Fatalf("groups.init --write: %v", e)
	}
	if written := rig.onDisk(); written != res.YAML {
		t.Errorf("the file and the returned yaml differ:\n%s\n---\n%s", written, res.YAML)
	}

	var listed rpc.GroupsListResult
	if e := rig.cl.call("groups.list", nil, &listed); e != nil {
		t.Fatalf("groups.list: %v", e)
	}
	g, found := groupNamed(listed.Groups, res.Proposal.Name)
	if !found {
		t.Fatalf("group %q is gone after init: %+v", res.Proposal.Name, listed.Groups)
	}
	if g.Source != "file" {
		t.Errorf("group source = %q, want file", g.Source)
	}
	if g.ConfigPath == nil || *g.ConfigPath != rig.file {
		t.Errorf("group config_path = %v, want %q", g.ConfigPath, rig.file)
	}
	// The group is on file now, and what it declares is what the file says:
	// nothing, until the user writes the list. The ports stay listed as members,
	// because those are what is running, not what is declared.
	if len(g.Services) != 0 {
		t.Fatalf("group services = %+v, want none: the file declares none", g.Services)
	}
}

// TestGroupsInitRefusesToOverwrite keeps the never-clobber rule on the wire: an
// existing file is never overwritten by accident, and the error names the way
// past it.
func TestGroupsInitRefusesToOverwrite(t *testing.T) {
	rig := newInitRig(t)

	const hand = "name: hand-written\n"
	if err := os.WriteFile(rig.file, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}

	// A dry run still answers: reading is never blocked by an existing file.
	if _, e := rig.init(rpc.GroupsInitParams{RootDir: rig.root}); e != nil {
		t.Fatalf("dry run over an existing file: %v", e)
	}

	_, e := rig.init(rpc.GroupsInitParams{RootDir: rig.root, Write: true})
	if e == nil {
		t.Fatal("writing over an existing file was not refused")
	}
	if e.Data.Code != "invalid_params" || !strings.Contains(e.Data.Hint, "force") {
		t.Errorf("error = %+v, want invalid_params hinting at force", e.Data)
	}
	if body := rig.onDisk(); body != hand {
		t.Errorf("the file was touched anyway:\n%s", body)
	}
}

// TestGroupsInitForceOverwrites is the other half of the same rule.
func TestGroupsInitForceOverwrites(t *testing.T) {
	rig := newInitRig(t)

	if err := os.WriteFile(rig.file, []byte("name: hand-written\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, e := rig.init(rpc.GroupsInitParams{RootDir: rig.root, Write: true, Force: true})
	if e != nil {
		t.Fatalf("groups.init --force: %v", e)
	}
	if body := rig.onDisk(); body != res.YAML || strings.Contains(body, "hand-written") {
		t.Errorf("force did not replace the file:\n%s", body)
	}
}

// TestGroupsInitRejectsABadRoot: the daemon has no working directory of the
// caller's, so a root it cannot use is a parameter error, not a guess.
func TestGroupsInitRejectsABadRoot(t *testing.T) {
	rig := newInitRig(t)

	file := filepath.Join(rig.root, "notadir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []struct {
		what string
		dir  string
		want string
	}{
		{"missing", "", "invalid_params"},
		{"relative", filepath.Join("relative", "path"), "invalid_params"},
		{"nonexistent", filepath.Join(rig.root, "nope", "nope"), "not_found"},
		{"not a directory", file, "invalid_params"},
	} {
		t.Run(bad.what, func(t *testing.T) {
			_, e := rig.init(rpc.GroupsInitParams{RootDir: bad.dir, Write: true})
			if e == nil {
				t.Fatalf("root_dir %q was accepted", bad.dir)
			}
			if e.Data.Code != bad.want {
				t.Errorf("error code = %q, want %q (%s)", e.Data.Code, bad.want, e.Data.Detail)
			}
		})
	}
}

// TestGroupsInitCuratesTheProposal: a caller that has let a user name the
// services writes the whole curated file in one call. The list is the caller's
// word for word: a port the caller kept does not get a command or a directory
// copied onto it from whatever happens to be listening there. That copy used to
// look helpful and was the bug — it produced entries whose command described
// one past run, so `oberth up` could not start them.
func TestGroupsInitCuratesTheProposal(t *testing.T) {
	rig := newInitRig(t)

	curated := []groups.ServiceAdd{
		{Name: "gateway", Port: 8000, Health: "/healthz", Color: "#4f8cc9", Description: "the api"},
		{Name: "storefront", Port: 5173},
		{Name: "worker", Port: 9000, Cmd: "uv run worker"},
	}
	res, e := rig.init(rpc.GroupsInitParams{RootDir: rig.root, Write: true, Services: curated})
	if e != nil {
		t.Fatalf("groups.init with services: %v", e)
	}
	if len(res.Affected) != 3 || res.Affected[0] != "gateway" {
		t.Errorf("affected = %v, want the curated names", res.Affected)
	}

	cfg := rig.load(res.YAML)
	if len(cfg.Services) != 3 {
		t.Fatalf("the file has %d services, want the three that were asked for:\n%s", len(cfg.Services), res.YAML)
	}
	gateway := cfg.Services[0]
	if gateway.Name != "gateway" || gateway.Health != "/healthz" ||
		gateway.Color != "#4f8cc9" || gateway.Description != "the api" {
		t.Errorf("gateway = %+v, want the caller's own metadata", gateway)
	}
	if gateway.Cmd != "" {
		t.Errorf("gateway cmd = %q, want empty: the caller stated none, and nothing listening on 8000 gets to state one", gateway.Cmd)
	}
	if storefront := cfg.Services[1]; storefront.Cwd != "" {
		t.Errorf("storefront cwd = %q, want empty: the caller stated none", storefront.Cwd)
	}
	if worker := cfg.Services[2]; worker.Cmd != "uv run worker" {
		t.Errorf("worker cmd = %q, want the caller's own", worker.Cmd)
	}

	// A service nothing is listening on is not reported as running.
	running := map[string]bool{}
	for _, s := range res.Proposal.Services {
		running[s.Name] = s.Running
	}
	if !running["gateway"] {
		t.Errorf("gateway should be running: port 8000 is listening")
	}
	if running["worker"] {
		t.Errorf("worker should not be running: nothing is on port 9000")
	}
	if res.Proposal.Status != "partial" {
		t.Errorf("proposal status = %q, want partial", res.Proposal.Status)
	}

	if written := rig.onDisk(); written != res.YAML {
		t.Errorf("the file on disk is not the yaml that was returned:\n%s", written)
	}
}

// TestGroupsInitCuratedListIsValidated: the service list is the caller's, so it
// is checked before anything is written rather than found by the next scan.
func TestGroupsInitCuratedListIsValidated(t *testing.T) {
	rig := newInitRig(t)

	_, e := rig.init(rpc.GroupsInitParams{
		RootDir:  rig.root,
		Write:    true,
		Services: []groups.ServiceAdd{{Name: "api", Port: 8000, DependsOn: []string{"nope"}}},
	})
	if e == nil || e.Data.Code != "invalid_config" {
		t.Fatalf("error = %+v, want invalid_config", e)
	}
	if _, err := os.Lstat(rig.file); err == nil {
		t.Error("the invalid file was written anyway")
	}

	blank := rpc.GroupsInitParams{
		RootDir:  rig.root,
		Services: []groups.ServiceAdd{{Name: "  ", Port: 8000}},
	}
	_, e = rig.init(blank)
	if e == nil || e.Data.Code != "invalid_params" {
		t.Fatalf("a service with a blank name: error = %+v, want invalid_params", e)
	}
}

// TestGroupsInitMergeAppends: `merge` is the way past an existing file that
// keeps what is in it. The file's comments and its order survive the append
// because it goes down the same node-level path as groups.config.set.
//
// What it appends is the caller's list; a plain proposal declares nothing, so
// there is nothing for it to append on its own — see
// TestGroupsInitMergeWithNothingToAdd.
func TestGroupsInitMergeAppends(t *testing.T) {
	rig := newInitRig(t)

	const hand = `# hand written, and it stays that way
name: demo
services:
  # the cache was here first
  - name: redis
    port: 6379
`
	if err := os.WriteFile(rig.file, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}

	adds := []groups.ServiceAdd{{Name: "api", Port: 8000}, {Name: "web", Port: 5173}}

	// A dry-run merge previews the merged file without touching it.
	res, e := rig.init(rpc.GroupsInitParams{RootDir: rig.root, Merge: true, Services: adds})
	if e != nil {
		t.Fatalf("groups.init --merge dry run: %v", e)
	}
	if !strings.Contains(res.YAML, "# the cache was here first") || !strings.Contains(res.YAML, "port: 8000") {
		t.Errorf("the previewed merge is wrong:\n%s", res.YAML)
	}
	if body := rig.onDisk(); body != hand {
		t.Errorf("a dry-run merge wrote to the file:\n%s", body)
	}

	res, e = rig.init(rpc.GroupsInitParams{RootDir: rig.root, Merge: true, Write: true, Services: adds})
	if e != nil {
		t.Fatalf("groups.init --merge: %v", e)
	}
	written := rig.onDisk()
	if written != res.YAML {
		t.Errorf("the file on disk is not the yaml that was returned:\n%s", written)
	}
	cfg := rig.load(written)
	if cfg.Name != "demo" {
		t.Errorf("name = %q, want the existing file's own", cfg.Name)
	}
	if len(cfg.Services) != 3 || cfg.Services[0].Name != "redis" {
		t.Fatalf("services = %+v, want the cache plus the two appended", cfg.Services)
	}
	if !strings.Contains(written, "# hand written, and it stays that way") ||
		!strings.Contains(written, "# the cache was here first") {
		t.Errorf("the comments were lost:\n%s", written)
	}
	if len(res.Affected) != 2 {
		t.Errorf("affected = %v, want only the appended services", res.Affected)
	}

	// Merging the same list again is a conflict, not a duplicate.
	_, e = rig.init(rpc.GroupsInitParams{RootDir: rig.root, Merge: true, Write: true, Services: adds})
	if e == nil || e.Data.Code != "conflict" {
		t.Fatalf("error = %+v, want conflict", e)
	}
}

// TestGroupsInitMergeWithNothingToAdd: a proposal declares nothing, so a merge
// with no service list has nothing to append. It has to say so rather than
// reporting a successful edit that changed nothing.
func TestGroupsInitMergeWithNothingToAdd(t *testing.T) {
	rig := newInitRig(t)

	const hand = "name: demo\nservices:\n  - name: queue\n    port: 6379\n"
	if err := os.WriteFile(rig.file, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}

	_, e := rig.init(rpc.GroupsInitParams{RootDir: rig.root, Merge: true, Write: true})
	if e == nil {
		t.Fatal("merging nothing into a file was accepted")
	}
	if body := rig.onDisk(); body != hand {
		t.Errorf("a refused merge changed the file:\n%s", body)
	}
}

// TestGroupsInitMergeOnANewFileJustWrites: merge is about not refusing, so with
// nothing there it behaves exactly like a plain init.
func TestGroupsInitMergeOnANewFileJustWrites(t *testing.T) {
	rig := newInitRig(t)

	res, e := rig.init(rpc.GroupsInitParams{RootDir: rig.root, Merge: true, Write: true})
	if e != nil {
		t.Fatalf("groups.init --merge on a new file: %v", e)
	}
	if body := rig.onDisk(); body != res.YAML || !strings.Contains(body, "written by") {
		t.Errorf("merge on a new file should write the proposal with its header:\n%s", body)
	}
}

// TestGroupsInitMergeAndForceAreExclusive: one replaces the file, the other
// keeps it; asking for both is a caller bug worth naming.
func TestGroupsInitMergeAndForceAreExclusive(t *testing.T) {
	rig := newInitRig(t)

	_, e := rig.init(rpc.GroupsInitParams{RootDir: rig.root, Write: true, Force: true, Merge: true})
	if e == nil || e.Data.Code != "invalid_params" {
		t.Fatalf("force together with merge: error = %+v, want invalid_params", e)
	}
}

// TestGroupsInitRefusalNamesMerge: the hint past an existing file has two ways
// out now, and the refusal has to name both of them.
func TestGroupsInitRefusalNamesMerge(t *testing.T) {
	rig := newInitRig(t)

	if err := os.WriteFile(rig.file, []byte("name: hand-written\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, e := rig.init(rpc.GroupsInitParams{RootDir: rig.root, Write: true})
	if e == nil {
		t.Fatal("writing over an existing file was not refused")
	}
	if !strings.Contains(e.Data.Hint, "force") || !strings.Contains(e.Data.Hint, "merge") {
		t.Errorf("hint = %q, want both ways out named", e.Data.Hint)
	}
}

// TestGroupsInitWritesAnEditedProposal: the preview pane is editable, so what
// the caller sends back has to be what lands on disk — byte for byte, with the
// name and the services it edited. Re-rendering an edit would silently undo it,
// which is worse than not offering the edit at all.
func TestGroupsInitWritesAnEditedProposal(t *testing.T) {
	rig := newInitRig(t)

	const edited = `# mine now
name: hand-edited
services:
  - name: api
    cmd: uv run api
    port: 8000
`
	res, e := rig.init(rpc.GroupsInitParams{RootDir: rig.root, Write: true, YAML: edited})
	if e != nil {
		t.Fatalf("groups.init with yaml: %v", e)
	}
	if body := rig.onDisk(); body != edited {
		t.Errorf("on disk:\n%s\n--- want:\n%s", body, edited)
	}
	if res.YAML != edited {
		t.Errorf("returned yaml is not the edited one:\n%s", res.YAML)
	}
	// What this call produced is the edited file, not the proposal it started
	// from: the name and the service list come from the bytes.
	if res.Proposal.Name != "hand-edited" {
		t.Errorf("proposal name = %q, want the edited one", res.Proposal.Name)
	}
	if len(res.Affected) != 1 || res.Affected[0] != "api" {
		t.Errorf("affected = %v, want the edited service", res.Affected)
	}
	if len(res.Proposal.Services) != 1 || !res.Proposal.Services[0].Running {
		t.Errorf("proposal services = %+v, want api running on 8000", res.Proposal.Services)
	}
}

// TestGroupsInitRefusesAnEditThatWillNotLoad: an edit is parsed before anything
// is written, so a broken one costs a message rather than a broken project.
func TestGroupsInitRefusesAnEditThatWillNotLoad(t *testing.T) {
	rig := newInitRig(t)

	_, e := rig.init(rpc.GroupsInitParams{
		RootDir: rig.root, Write: true,
		YAML: "name: demo\nservices:\n  - name: api\n    port: not-a-number\n",
	})
	if e == nil || e.Data.Code != "invalid_config" {
		t.Fatalf("error = %+v, want invalid_config", e)
	}
	if _, err := os.Lstat(rig.file); err == nil {
		t.Error("a broken edit was written anyway")
	}

	// A name a group cannot have is the same kind of refusal. (A *missing* name
	// is not one: a file without it takes the directory's name.)
	_, e = rig.init(rpc.GroupsInitParams{RootDir: rig.root, Write: true, YAML: "name: not a name\n"})
	if e == nil || e.Data.Code != "invalid_config" {
		t.Fatalf("error = %+v, want invalid_config for a name with a space", e)
	}
}

// TestGroupsInitRefusesYAMLNextToAList: a whole file and a list option-berth
// renders are two different answers to "who writes this". Taking both would
// mean silently dropping one of them.
func TestGroupsInitRefusesYAMLNextToAList(t *testing.T) {
	rig := newInitRig(t)

	for _, p := range []rpc.GroupsInitParams{
		{RootDir: rig.root, YAML: "name: demo\n", Merge: true},
		{RootDir: rig.root, YAML: "name: demo\n", Services: []groups.ServiceAdd{{Name: "api", Port: 8000}}},
	} {
		if _, e := rig.init(p); e == nil || e.Data.Code != "invalid_params" {
			t.Errorf("params %+v: error = %+v, want invalid_params", p, e)
		}
	}
}

// TestInitSnapshotTrustsAWarmCacheOnlyWhileSubscribed pins the rule that keeps
// the proposal and a live subscriber's deltas from disagreeing: the cached
// snapshot is used directly only when it exists and somebody is reading it.
// Otherwise the call reads state the ordinary way, and an absent cache is never
// mistaken for a warm one.
func TestInitSnapshotTrustsAWarmCacheOnlyWhileSubscribed(t *testing.T) {
	countingRuntime := func(t *testing.T, scans *int) (*Runtime, *scanner.Loop) {
		t.Helper()
		loop := scanner.New(scanner.Options{
			DaemonVersion: "test",
			Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
				*scans++
				return []ports.ListeningPort{{Port: 8000, PID: 1, Process: "python3"}}, nil
			},
		})
		return New(Options{Socket: "/test/init-snapshot.sock", Version: "test", Scanner: loop}).Runtime(), loop
	}

	t.Run("no subscriber forces a read", func(t *testing.T) {
		scans := 0
		rt, loop := countingRuntime(t, &scans)
		if _, err := loop.Snapshot(scanner.Include{}); err != nil {
			t.Fatal(err)
		}
		if loop.Cached().Seq == 0 {
			t.Fatal("test setup: the cache has no sequence")
		}
		loop.Invalidate() // a warm cache nobody is reading must still be refreshed
		before := scans
		if _, err := initSnapshot(rt); err != nil {
			t.Fatalf("initSnapshot: %v", err)
		}
		if scans == before {
			t.Fatal("initSnapshot trusted a warm cache while nobody was subscribed")
		}
	})

	t.Run("a subscriber keeps it warm", func(t *testing.T) {
		scans := 0
		rt, loop := countingRuntime(t, &scans)
		if _, err := loop.Snapshot(scanner.Include{}); err != nil {
			t.Fatal(err)
		}
		reader := newConn(1, rt.srv, nil)
		reader.subscribed = true
		rt.srv.registerConn(reader)
		loop.Invalidate()
		before := scans
		snap, err := initSnapshot(rt)
		if err != nil {
			t.Fatalf("initSnapshot: %v", err)
		}
		if scans != before {
			t.Fatal("initSnapshot rescanned despite a subscriber keeping the cache warm")
		}
		if snap.Seq != 1 {
			t.Fatalf("returned sequence %d, want the warm cached 1", snap.Seq)
		}
	})

	t.Run("an empty cache is never warm", func(t *testing.T) {
		scans := 0
		rt, _ := countingRuntime(t, &scans)
		reader := newConn(1, rt.srv, nil)
		reader.subscribed = true
		rt.srv.registerConn(reader)
		if _, err := initSnapshot(rt); err != nil {
			t.Fatalf("initSnapshot: %v", err)
		}
		if scans == 0 {
			t.Fatal("initSnapshot treated an empty cache as a warm one")
		}
	})
}

// TestPublishConfigWarnsOnlyOnReloadFailure pins both halves of the reload
// branch: a config that indexes cleanly publishes silently, and one that does
// not is reported with its real error while the call still finishes publishing
// and reporting the write. A quiet success and a loud failure are the same
// branch seen from the two sides, so a test that only covers one leaves the
// comparison free to invert.
func TestPublishConfigWarnsOnlyOnReloadFailure(t *testing.T) {
	rig := newInitRig(t)

	proposal := rig.proposal()
	if err := os.WriteFile(rig.file, []byte(proposal.YAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// The runtime publishConfig needs is the scanner and a logger; giving it a
	// private one keeps this test's diagnostics out of the daemon's log.
	var logged bytes.Buffer
	rt := &Runtime{Scanner: rig.h.loop, Logger: slog.New(slog.NewTextHandler(&logged, nil))}
	req := &Request{Runtime: rt}

	publishConfig(req, rig.file, proposal.Proposal.Name, 0)
	if strings.Contains(logged.String(), "reloading a config after writing it") {
		t.Fatalf("a successful reload warned: %s", logged.String())
	}
	var listed rpc.GroupsListResult
	if e := rig.cl.call("groups.list", nil, &listed); e != nil {
		t.Fatalf("groups.list: %v", e)
	}
	if g, ok := groupNamed(listed.Groups, proposal.Proposal.Name); !ok || g.Source != "file" {
		t.Fatalf("the written config was not published: %+v", listed.Groups)
	}

	// A file the index cannot parse must be reported with the error it failed
	// on, not swallowed, and the write itself is still reported afterwards.
	if err := os.WriteFile(rig.file, []byte("name: [unterminated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logged.Reset()
	publishConfig(req, rig.file, proposal.Proposal.Name, 0)
	text := logged.String()
	if !strings.Contains(text, "level=WARN") ||
		!strings.Contains(text, "reloading a config after writing it") ||
		!strings.Contains(text, "error=") || strings.Contains(text, "error=<nil>") {
		t.Fatalf("failed reload warning = %s", text)
	}
	if !strings.Contains(text, "wrote "+groups.ConfigName) {
		t.Fatalf("the reload error suppressed the write report: %s", text)
	}
}
