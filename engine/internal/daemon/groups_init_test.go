package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/scanner"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// initHarness gives a test a daemon that can see two services listening inside
// a git checkout that has no `.oberth.yaml` yet — the state `groups.init` exists
// to get out of.
func initHarness(t *testing.T, ctx context.Context) (*testHarness, string, string) {
	t.Helper()
	root := resolvedDir(t, t.TempDir())
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, ctx)
	h.setRows(
		ports.ListeningPort{
			Port: 8000, BindAddress: "127.0.0.1", PID: 4242,
			Process: "python", Display: "api", Command: "uv run api", Cwd: root,
		},
		ports.ListeningPort{
			Port: 5173, BindAddress: "127.0.0.1", PID: 4243,
			Process: "node", Display: "web", Command: "npm run dev",
			Cwd: filepath.Join(root, "web"),
		},
		// Somebody else's process, in somebody else's directory: it must not
		// end up in this project's file.
		ports.ListeningPort{
			Port: 9999, BindAddress: "127.0.0.1", PID: 4244,
			Process: "other", Display: "other", Command: "other", Cwd: resolvedDir(t, t.TempDir()),
		},
	)
	if _, err := h.loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatalf("priming the scanner: %v", err)
	}
	return h, root, filepath.Join(root, groups.ConfigName)
}

// callInit is the method under test, with the params a caller sends.
func callInit(t *testing.T, c *testClient, p rpc.GroupsInitParams) (rpc.GroupsInitResult, *rpc.Error) {
	t.Helper()
	var res rpc.GroupsInitResult
	e := c.call("groups.init", p, &res)
	return res, e
}

// TestGroupsInitDryRunReturnsAParsableProposal is the default call: no write,
// and what comes back is the exact file that would have been written.
func TestGroupsInitDryRunReturnsAParsableProposal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, target := initHarness(t, ctx)
	c := h.dial(ctx)

	res, e := callInit(t, c, rpc.GroupsInitParams{RootDir: root})
	if e != nil {
		t.Fatalf("groups.init: %v", e)
	}
	if !res.OK || res.Path != target {
		t.Fatalf("result = %+v, want ok with path %q", res, target)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("a dry run must not write %s (err = %v)", target, err)
	}
	if !strings.Contains(res.YAML, "oberth init") {
		t.Errorf("yaml is missing the generated header:\n%s", res.YAML)
	}

	// The proposal has to survive the loader, or `write: true` would put an
	// unusable file on disk.
	cfg := loadYAML(t, res.YAML)
	if cfg.Name != res.Proposal.Name {
		t.Errorf("parsed name %q, proposal says %q", cfg.Name, res.Proposal.Name)
	}
	// Nothing is declared: two ports are listening in the checkout, and neither
	// becomes a service. option-berth has no way to state how to start either one
	// — the command line a process was launched with is a snapshot of that run.
	if len(cfg.Services) != 0 {
		t.Fatalf("parsed services = %+v, want none: a proposal declares nothing", cfg.Services)
	}
	if got := servicePorts(t, res.YAML); len(got) != 0 {
		t.Errorf("declared ports = %v, want none", got)
	}
	if len(res.Affected) != 0 {
		t.Errorf("affected = %v, want none — no service was written", res.Affected)
	}
	// What it saw is still reported, as the context whoever writes the list needs.
	for _, want := range []string{"8000", "5173", "api", "web"} {
		if !strings.Contains(res.YAML, want) {
			t.Errorf("the file does not mention %q as context:\n%s", want, res.YAML)
		}
	}
	if res.Proposal.ConfigPath == nil || *res.Proposal.ConfigPath != target {
		t.Errorf("proposal config_path = %v, want %q", res.Proposal.ConfigPath, target)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, _ := initHarness(t, ctx)
	c := h.dial(ctx)

	if err := os.WriteFile(filepath.Join(root, "compose.yaml"),
		[]byte("services:\n  db:\n    image: postgres\n    ports: [\"5432:5432\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"),
		[]byte(`{"scripts":{"dev":"vite"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res, e := callInit(t, c, rpc.GroupsInitParams{RootDir: root})
	if e != nil {
		t.Fatalf("groups.init: %v", e)
	}
	cfg := loadYAML(t, res.YAML)
	if len(cfg.Services) != 0 {
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, target := initHarness(t, ctx)
	c := h.dial(ctx)

	res, e := callInit(t, c, rpc.GroupsInitParams{RootDir: root, Write: true})
	if e != nil {
		t.Fatalf("groups.init: %v", e)
	}
	onDisk, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading what the daemon wrote: %v", err)
	}
	if string(onDisk) != res.YAML {
		t.Errorf("the file and the returned yaml differ:\n%s\n---\n%s", onDisk, res.YAML)
	}

	var list rpc.GroupsListResult
	if e := c.call("groups.list", nil, &list); e != nil {
		t.Fatalf("groups.list: %v", e)
	}
	g, ok := groupNamed(list.Groups, res.Proposal.Name)
	if !ok {
		t.Fatalf("group %q is gone after init: %+v", res.Proposal.Name, list.Groups)
	}
	if g.Source != "file" {
		t.Errorf("group source = %q, want file", g.Source)
	}
	if g.ConfigPath == nil || *g.ConfigPath != target {
		t.Errorf("group config_path = %v, want %q", g.ConfigPath, target)
	}
	// The group is on file now, and what it declares is what the file says:
	// nothing, until the user writes the list. The ports are still listed as
	// members, because those are what is running, not what is declared.
	if len(g.Services) != 0 {
		t.Fatalf("group services = %+v, want none: the file declares none", g.Services)
	}
}

// TestGroupsInitRefusesToOverwrite keeps §16's rule on the wire: an existing
// file is never clobbered by accident, and the error names the way past it.
func TestGroupsInitRefusesToOverwrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, target := initHarness(t, ctx)
	c := h.dial(ctx)

	const hand = "name: hand-written\n"
	if err := os.WriteFile(target, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}

	// A dry run still answers: reading is never blocked by an existing file.
	if _, e := callInit(t, c, rpc.GroupsInitParams{RootDir: root}); e != nil {
		t.Fatalf("dry run over an existing file: %v", e)
	}

	_, e := callInit(t, c, rpc.GroupsInitParams{RootDir: root, Write: true})
	if e == nil {
		t.Fatal("writing over an existing file should have been refused")
	}
	if e.Data.Code != "invalid_params" || !strings.Contains(e.Data.Hint, "force") {
		t.Errorf("error = %+v, want invalid_params hinting at force", e.Data)
	}
	if b, _ := os.ReadFile(target); string(b) != hand {
		t.Errorf("the file was touched anyway:\n%s", b)
	}
}

// TestGroupsInitForceOverwrites is the other half of the same rule.
func TestGroupsInitForceOverwrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, target := initHarness(t, ctx)
	c := h.dial(ctx)

	if err := os.WriteFile(target, []byte("name: hand-written\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, e := callInit(t, c, rpc.GroupsInitParams{RootDir: root, Write: true, Force: true})
	if e != nil {
		t.Fatalf("groups.init --force: %v", e)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != res.YAML || strings.Contains(string(b), "hand-written") {
		t.Errorf("force did not replace the file:\n%s", b)
	}
}

// TestGroupsInitRejectsABadRoot: the daemon has no working directory of the
// caller's, so a root it cannot use is a parameter error, not a guess.
func TestGroupsInitRejectsABadRoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, _ := initHarness(t, ctx)
	c := h.dial(ctx)

	file := filepath.Join(root, "notadir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		root string
		code string
	}{
		{"missing", "", "invalid_params"},
		{"relative", filepath.Join("relative", "path"), "invalid_params"},
		{"nonexistent", filepath.Join(root, "nope", "nope"), "not_found"},
		{"not a directory", file, "invalid_params"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, e := callInit(t, c, rpc.GroupsInitParams{RootDir: tc.root, Write: true})
			if e == nil {
				t.Fatalf("root_dir %q should have been refused", tc.root)
			}
			if e.Data.Code != tc.code {
				t.Errorf("error code = %q, want %q (%s)", e.Data.Code, tc.code, e.Data.Detail)
			}
		})
	}
}

// loadYAML writes a proposal to a scratch file and reads it back through the
// real loader, which is the check that matters: the daemon must never propose
// something `groups.Load` would reject.
func loadYAML(t *testing.T, text string) *groups.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), groups.ConfigName)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := groups.Load(path)
	if err != nil {
		t.Fatalf("the proposal does not load: %v\n%s", err, text)
	}
	return cfg
}

// servicePorts is the proposed ports, sorted, read back out of the YAML.
func servicePorts(t *testing.T, text string) []int {
	t.Helper()
	cfg := loadYAML(t, text)
	out := make([]int, 0, len(cfg.Services))
	for _, s := range cfg.Services {
		out = append(out, s.Port)
	}
	return out
}

func groupNamed(gg []state.Group, name string) (state.Group, bool) {
	for _, g := range gg {
		if g.Name == name {
			return g, true
		}
	}
	return state.Group{}, false
}

// TestGroupsInitCuratesTheProposal: a caller that has let a user name the
// services writes the whole curated file in one call (step 5A.4). The list is
// the caller's word for word: a port the caller kept does not get a command or
// a directory copied onto it from whatever happens to be listening there. That
// copy used to look helpful and was the bug — it produced entries whose command
// described one past run, so `oberth up` could not start them.
func TestGroupsInitCuratesTheProposal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, target := initHarness(t, ctx)
	c := h.dial(ctx)

	res, e := callInit(t, c, rpc.GroupsInitParams{
		RootDir: root,
		Write:   true,
		Services: []groups.ServiceAdd{
			{Name: "gateway", Port: 8000, Health: "/healthz", Color: "#4f8cc9", Description: "the api"},
			{Name: "storefront", Port: 5173},
			{Name: "worker", Port: 9000, Cmd: "uv run worker"},
		},
	})
	if e != nil {
		t.Fatalf("groups.init with services: %v", e)
	}
	if len(res.Affected) != 3 || res.Affected[0] != "gateway" {
		t.Errorf("affected = %v, want the curated names", res.Affected)
	}

	cfg := loadYAML(t, res.YAML)
	if len(cfg.Services) != 3 {
		t.Fatalf("the file has %d services, want the three that were asked for:\n%s", len(cfg.Services), res.YAML)
	}
	if cfg.Services[0].Name != "gateway" || cfg.Services[0].Health != "/healthz" ||
		cfg.Services[0].Color != "#4f8cc9" || cfg.Services[0].Description != "the api" {
		t.Errorf("gateway = %+v, want the caller's own metadata", cfg.Services[0])
	}
	if cfg.Services[0].Cmd != "" {
		t.Errorf("gateway cmd = %q, want empty: the caller stated none, and nothing listening on 8000 gets to state one", cfg.Services[0].Cmd)
	}
	if cfg.Services[1].Cwd != "" {
		t.Errorf("storefront cwd = %q, want empty: the caller stated none", cfg.Services[1].Cwd)
	}
	if cfg.Services[2].Cmd != "uv run worker" {
		t.Errorf("worker cmd = %q, want the caller's own", cfg.Services[2].Cmd)
	}

	// A service nothing is listening on is not reported as running.
	byName := map[string]state.Service{}
	for _, s := range res.Proposal.Services {
		byName[s.Name] = s
	}
	if !byName["gateway"].Running {
		t.Errorf("gateway should be running: port 8000 is listening")
	}
	if byName["worker"].Running {
		t.Errorf("worker should not be running: nothing is on port 9000")
	}
	if res.Proposal.Status != "partial" {
		t.Errorf("proposal status = %q, want partial", res.Proposal.Status)
	}

	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != res.YAML {
		t.Errorf("the file on disk is not the yaml that was returned:\n%s", written)
	}
}

// TestGroupsInitCuratedListIsValidated: the service list is the caller's, so it
// is checked before anything is written rather than found by the next scan.
func TestGroupsInitCuratedListIsValidated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, target := initHarness(t, ctx)
	c := h.dial(ctx)

	_, e := callInit(t, c, rpc.GroupsInitParams{
		RootDir:  root,
		Write:    true,
		Services: []groups.ServiceAdd{{Name: "api", Port: 8000, DependsOn: []string{"nope"}}},
	})
	if e == nil || e.Data.Code != "invalid_config" {
		t.Fatalf("error = %+v, want invalid_config", e)
	}
	if _, err := os.Lstat(target); err == nil {
		t.Error("the invalid file was written anyway")
	}

	_, e = callInit(t, c, rpc.GroupsInitParams{
		RootDir:  root,
		Services: []groups.ServiceAdd{{Name: "  ", Port: 8000}},
	})
	if e == nil || e.Data.Code != "invalid_params" {
		t.Fatalf("error = %+v, want invalid_params for an empty name", e)
	}
}

// TestGroupsInitMergeAppends: `merge` is the way past an existing file that
// keeps what is in it, and the file's comments and order come through the
// append because it goes down the same node-level path as groups.config.set.
//
// What it appends is the caller's list. A plain proposal declares nothing, so
// there is nothing for it to append on its own — see
// TestGroupsInitMergeWithNothingToAdd.
func TestGroupsInitMergeAppends(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, target := initHarness(t, ctx)
	c := h.dial(ctx)

	const hand = `# hand written, and it stays that way
name: demo
services:
  # the queue was here first
  - name: queue
    port: 6379
`
	if err := os.WriteFile(target, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}

	adds := []groups.ServiceAdd{{Name: "api", Port: 8000}, {Name: "web", Port: 5173}}

	// A dry-run merge previews the merged file without touching it.
	res, e := callInit(t, c, rpc.GroupsInitParams{RootDir: root, Merge: true, Services: adds})
	if e != nil {
		t.Fatalf("groups.init --merge dry run: %v", e)
	}
	if !strings.Contains(res.YAML, "# the queue was here first") || !strings.Contains(res.YAML, "port: 8000") {
		t.Errorf("the previewed merge is wrong:\n%s", res.YAML)
	}
	if b, _ := os.ReadFile(target); string(b) != hand {
		t.Errorf("a dry-run merge wrote to the file:\n%s", b)
	}

	res, e = callInit(t, c, rpc.GroupsInitParams{RootDir: root, Merge: true, Write: true, Services: adds})
	if e != nil {
		t.Fatalf("groups.init --merge: %v", e)
	}
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != res.YAML {
		t.Errorf("the file on disk is not the yaml that was returned:\n%s", written)
	}
	cfg := loadYAML(t, string(written))
	if cfg.Name != "demo" {
		t.Errorf("name = %q, want the existing file's own", cfg.Name)
	}
	if len(cfg.Services) != 3 || cfg.Services[0].Name != "queue" {
		t.Fatalf("services = %+v, want the queue plus the two appended", cfg.Services)
	}
	if !strings.Contains(string(written), "# hand written, and it stays that way") ||
		!strings.Contains(string(written), "# the queue was here first") {
		t.Errorf("the comments were lost:\n%s", written)
	}
	if len(res.Affected) != 2 {
		t.Errorf("affected = %v, want only the appended services", res.Affected)
	}

	// Merging the same list again is a conflict, not a duplicate.
	_, e = callInit(t, c, rpc.GroupsInitParams{RootDir: root, Merge: true, Write: true, Services: adds})
	if e == nil || e.Data.Code != "conflict" {
		t.Fatalf("error = %+v, want conflict", e)
	}
}

// TestGroupsInitMergeWithNothingToAdd: a proposal declares nothing, so a merge
// with no service list has nothing to append. It has to say so rather than
// reporting a successful edit that changed nothing.
func TestGroupsInitMergeWithNothingToAdd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, target := initHarness(t, ctx)
	c := h.dial(ctx)

	const hand = "name: demo\nservices:\n  - name: queue\n    port: 6379\n"
	if err := os.WriteFile(target, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}

	_, e := callInit(t, c, rpc.GroupsInitParams{RootDir: root, Merge: true, Write: true})
	if e == nil {
		t.Fatal("merging nothing into a file should have been refused")
	}
	if b, _ := os.ReadFile(target); string(b) != hand {
		t.Errorf("a refused merge changed the file:\n%s", b)
	}
}

// TestGroupsInitMergeOnANewFileJustWrites: merge is about not refusing, so with
// nothing there it behaves exactly like a plain init.
func TestGroupsInitMergeOnANewFileJustWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, target := initHarness(t, ctx)
	c := h.dial(ctx)

	res, e := callInit(t, c, rpc.GroupsInitParams{RootDir: root, Merge: true, Write: true})
	if e != nil {
		t.Fatalf("groups.init --merge on a new file: %v", e)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != res.YAML || !strings.Contains(string(b), "written by") {
		t.Errorf("merge on a new file should write the proposal with its header:\n%s", b)
	}
}

// TestGroupsInitMergeAndForceAreExclusive: one replaces the file, the other
// keeps it; asking for both is a caller bug worth naming.
func TestGroupsInitMergeAndForceAreExclusive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, _ := initHarness(t, ctx)
	c := h.dial(ctx)

	_, e := callInit(t, c, rpc.GroupsInitParams{RootDir: root, Write: true, Force: true, Merge: true})
	if e == nil || e.Data.Code != "invalid_params" {
		t.Fatalf("error = %+v, want invalid_params", e)
	}
}

// TestGroupsInitRefusalNamesMerge: the hint past an existing file now has two
// ways out, and both have to be in it.
func TestGroupsInitRefusalNamesMerge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, target := initHarness(t, ctx)
	c := h.dial(ctx)

	if err := os.WriteFile(target, []byte("name: hand-written\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, e := callInit(t, c, rpc.GroupsInitParams{RootDir: root, Write: true})
	if e == nil {
		t.Fatal("writing over an existing file should have been refused")
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, target := initHarness(t, ctx)
	c := h.dial(ctx)

	const edited = `# mine now
name: hand-edited
services:
  - name: api
    cmd: uv run api
    port: 8000
`
	res, e := callInit(t, c, rpc.GroupsInitParams{RootDir: root, Write: true, YAML: edited})
	if e != nil {
		t.Fatalf("groups.init with yaml: %v", e)
	}
	onDisk, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != edited {
		t.Errorf("on disk:\n%s\n--- want:\n%s", onDisk, edited)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, target := initHarness(t, ctx)
	c := h.dial(ctx)

	_, e := callInit(t, c, rpc.GroupsInitParams{
		RootDir: root, Write: true,
		YAML: "name: demo\nservices:\n  - name: api\n    port: not-a-number\n",
	})
	if e == nil || e.Data.Code != "invalid_config" {
		t.Fatalf("error = %+v, want invalid_config", e)
	}
	if _, err := os.Lstat(target); err == nil {
		t.Error("a broken edit was written anyway")
	}

	// A name a group cannot have is the same kind of refusal. (A *missing* name
	// is not one: a file without it takes the directory's name.)
	_, e = callInit(t, c, rpc.GroupsInitParams{RootDir: root, Write: true, YAML: "name: not a name\n"})
	if e == nil || e.Data.Code != "invalid_config" {
		t.Fatalf("error = %+v, want invalid_config for a name with a space", e)
	}
}

// TestGroupsInitRefusesYAMLNextToAList: a whole file and a list option-berth
// renders are two different answers to "who writes this". Taking both would
// mean silently dropping one of them.
func TestGroupsInitRefusesYAMLNextToAList(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, _ := initHarness(t, ctx)
	c := h.dial(ctx)

	for _, p := range []rpc.GroupsInitParams{
		{RootDir: root, YAML: "name: demo\n", Merge: true},
		{RootDir: root, YAML: "name: demo\n", Services: []groups.ServiceAdd{{Name: "api", Port: 8000}}},
	} {
		_, e := callInit(t, c, p)
		if e == nil || e.Data.Code != "invalid_params" {
			t.Errorf("params %+v: error = %+v, want invalid_params", p, e)
		}
	}
}
