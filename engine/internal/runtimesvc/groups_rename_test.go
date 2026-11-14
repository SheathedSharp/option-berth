package runtimesvc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// renameTree is a project checked out twice — its main checkout and one linked
// worktree — sitting next to an unrelated repository whose name is already
// taken.
type renameTree struct {
	main  string // the primary checkout: the project itself
	wt    string // the linked worktree: the same project on one branch
	taken string // somebody else's repository, named "taken"
}

func newRenameTree(t *testing.T, withManifest bool) renameTree {
	t.Helper()
	root := resolvedDir(t, t.TempDir())
	tree := renameTree{
		main:  filepath.Join(root, "shop"),
		wt:    filepath.Join(root, "wt", "feat"),
		taken: filepath.Join(root, "taken"),
	}
	admin := filepath.Join(tree.main, ".git", "worktrees", "feat")
	files := map[string]string{
		filepath.Join(tree.main, ".git", "HEAD"): "ref: refs/heads/main\n",
		filepath.Join(admin, "HEAD"):             "ref: refs/heads/feat\n",
		filepath.Join(tree.wt, ".git"):           "gitdir: " + admin + "\n",
	}
	if withManifest {
		files[filepath.Join(tree.main, groups.ConfigName)] = "# the shop\nname: shop\n"
		files[filepath.Join(tree.wt, groups.ConfigName)] = "name: shop\n"
	}
	// Writing each file creates its own directory: the worktree's gitdir lives
	// inside the main checkout's .git, so the map lays out all three trees.
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(tree.taken, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return tree
}

// rows is one listening process in each of the three trees, so every group the
// resolver derives has a member.
func (tree renameTree) rows() []ports.ListeningPort {
	listening := func(port, pid int, cwd string) ports.ListeningPort {
		return ports.ListeningPort{Port: port, BindAddress: "127.0.0.1", PID: pid, Process: "node", Cwd: cwd}
	}
	return []ports.ListeningPort{
		listening(3000, 501, tree.main),
		listening(3001, 502, tree.wt),
		listening(4000, 503, tree.taken),
	}
}

// groupsByName reads groups.list and keys the answer by group name, refusing a
// list that names one group twice.
func (c *testClient) groupsByName(t *testing.T) map[string]state.Group {
	t.Helper()
	var res rpc.GroupsListResult
	if e := c.call("groups.list", rpc.Empty{}, &res); e != nil {
		t.Fatalf("groups.list: %v", e)
	}
	byName := make(map[string]state.Group, len(res.Groups))
	for _, g := range res.Groups {
		if _, clash := byName[g.Name]; clash {
			t.Fatalf("groups.list names %q twice", g.Name)
		}
		byName[g.Name] = g
	}
	return byName
}

// renameFindingDelta sends groups.rename and returns its result with the last
// groups-moving state.delta that arrived before the reply — the delta
// republish promises is queued ahead of the response.
func (c *testClient) renameFindingDelta(t *testing.T, p rpc.GroupsRenameParams) (rpc.GroupsRenameResult, state.Delta) {
	t.Helper()
	id := c.send("groups.rename", p)
	var (
		latest  state.Delta
		carried bool
	)
	for range 50 {
		m := c.read()
		if m.IsNotification() && m.Method == rpc.MethodStateDelta {
			var d state.Delta
			if err := json.Unmarshal(m.Params, &d); err != nil {
				t.Fatalf("decoding state.delta: %v", err)
			}
			if len(d.Groups.Added)+len(d.Groups.Removed) > 0 {
				latest, carried = d, true
			}
			continue
		}
		if !m.IsResponse() || string(m.ID) != id {
			continue
		}
		if m.Error != nil {
			t.Fatalf("groups.rename: %v", m.Error)
		}
		if !carried {
			t.Fatal("the reply arrived before any delta carrying the renamed groups")
		}
		var res rpc.GroupsRenameResult
		if err := json.Unmarshal(m.Result, &res); err != nil {
			t.Fatalf("decoding the rename result: %v", err)
		}
		return res, latest
	}
	t.Fatal("no reply to groups.rename")
	return rpc.GroupsRenameResult{}, state.Delta{}
}

// sortedNames joins the group names in one comma-separated, sorted string, so
// a whole delta can be compared in one assertion.
func sortedNames(gg []state.Group) string {
	names := make([]string, len(gg))
	for i, g := range gg {
		names[i] = g.Name
	}
	slices.Sort(names)
	return strings.Join(names, ",")
}

func TestGroupsRenameStoresAnAliasForAProjectWithoutAFile(t *testing.T) {
	tree := newRenameTree(t, false)
	h, st := storeHarness(t, tree.rows()...)
	cl := h.dial(t.Context())
	cl.subscribeAndSettle(rpc.StateSubscribeParams{})

	before := cl.groupsByName(t)
	if wt, ok := before["shop@feat"]; !ok || wt.Repo != "shop" || wt.Worktree != "feat" || wt.Branch != "feat" {
		t.Fatalf("worktree group before = %+v (%v)", wt, ok)
	}
	if main := before["shop"]; main.Repo != "shop" || main.Worktree != "" || main.Branch != "main" {
		t.Fatalf("main group before = %+v", main)
	}

	// The worktree group is named through a checkout, so the alias it stores
	// has to land on the main one.
	res, delta := cl.renameFindingDelta(t, rpc.GroupsRenameParams{Name: "shop@feat", To: "market"})
	if !res.OK || res.Name != "market" || strings.Join(res.Affected, ",") != "market,market@feat" {
		t.Fatalf("result = %+v", res)
	}
	removed := slices.Clone(delta.Groups.Removed)
	slices.Sort(removed)
	if got := strings.Join(removed, ","); got != "shop,shop@feat" {
		t.Errorf("delta removed %q, want the old names", got)
	}
	if added := sortedNames(delta.Groups.Added); added != "market,market@feat" {
		t.Errorf("delta added %q, want the new names", added)
	}

	aliases, err := st.GroupAliases()
	if err != nil {
		t.Fatalf("reading the alias table: %v", err)
	}
	if aliases[tree.main] != "market" {
		t.Errorf("aliases = %v, want the main checkout named market", aliases)
	}

	after := cl.groupsByName(t)
	if _, still := after["shop"]; still {
		t.Error("the old project name is still listed")
	}
	if g := after["market@feat"]; g.Repo != "market" || g.Worktree != "feat" || !slices.Equal(g.Members, []int{3001}) {
		t.Errorf("renamed worktree = %+v", g)
	}
	if g := after["market"]; !slices.Equal(g.Members, []int{3000}) {
		t.Errorf("renamed main = %+v", g)
	}
	if _, ok := after["taken"]; !ok {
		t.Error("the unrelated project went missing")
	}
}

func TestGroupsRenameWritesTheMainCheckoutsFile(t *testing.T) {
	tree := newRenameTree(t, true)
	h, st := storeHarness(t, tree.rows()...)
	cl := h.dial(t.Context())

	if g := cl.groupsByName(t)["shop@feat"]; g.Source != state.SourceFile ||
		g.ConfigPath == nil || *g.ConfigPath != filepath.Join(tree.wt, groups.ConfigName) {
		t.Fatalf("worktree group before = %+v", g)
	}

	var res rpc.GroupsRenameResult
	if e := cl.call("groups.rename", rpc.GroupsRenameParams{Name: "shop", To: "market"}, &res); e != nil {
		t.Fatalf("renaming shop: %v", e)
	}
	if got := strings.Join(res.Affected, ","); got != "market,market@feat" {
		t.Fatalf("affected = %q, want the project and its worktree", got)
	}

	mainFile, _ := os.ReadFile(filepath.Join(tree.main, groups.ConfigName))
	if !strings.Contains(string(mainFile), "# the shop") || !strings.Contains(string(mainFile), "name: market") {
		t.Errorf("the main checkout's manifest =\n%s", mainFile)
	}
	// The worktree's copy is not the project's name; it is left alone.
	if wtFile, _ := os.ReadFile(filepath.Join(tree.wt, groups.ConfigName)); string(wtFile) != "name: shop\n" {
		t.Errorf("the worktree's copy was rewritten:\n%s", wtFile)
	}
	if aliases, _ := st.GroupAliases(); len(aliases) != 0 {
		t.Errorf("a file project stored an alias: %v", aliases)
	}

	after := cl.groupsByName(t)
	for _, name := range []string{"market", "market@feat"} {
		if _, ok := after[name]; !ok {
			t.Errorf("%s missing after the rename: %v", name, after)
		}
	}
	if _, still := after["shop"]; still {
		t.Error("the old project name is still listed")
	}

	// And by name, the renamed project's config is found again.
	name := "market@feat"
	var got rpc.GroupsConfigGetResult
	if e := cl.call("groups.config.get", rpc.GroupsConfigGetParams{Name: &name}, &got); e != nil {
		t.Fatalf("groups.config.get market@feat: %v", e)
	}
	if got.Path != filepath.Join(tree.wt, groups.ConfigName) {
		t.Errorf("config for market@feat = %s, want the worktree's copy", got.Path)
	}
}

func TestGroupsRenameRefuses(t *testing.T) {
	tree := newRenameTree(t, false)
	h, _ := storeHarness(t, tree.rows()...)
	cl := h.dial(t.Context())

	refusals := []struct {
		what string
		call rpc.GroupsRenameParams
		code string
	}{
		{"without a name", rpc.GroupsRenameParams{To: "market"}, "invalid_params"},
		{"without a new name", rpc.GroupsRenameParams{Name: "shop"}, "invalid_params"},
		{"an @ inside the new name", rpc.GroupsRenameParams{Name: "shop", To: "a@b"}, "invalid_params"},
		{"a slash inside the new name", rpc.GroupsRenameParams{Name: "shop", To: "a/b"}, "invalid_params"},
		{"a group that is not there", rpc.GroupsRenameParams{Name: "nope", To: "market"}, "not_found"},
		{"a name another project already has", rpc.GroupsRenameParams{Name: "shop", To: "taken"}, "conflict"},
	}
	for _, refusal := range refusals {
		t.Run(refusal.what, func(t *testing.T) {
			e := cl.call("groups.rename", refusal.call, nil)
			if e == nil || e.Data.Code != refusal.code {
				t.Fatalf("error = %+v, want %s", e, refusal.code)
			}
		})
	}
	if _, ok := cl.groupsByName(t)["shop"]; !ok {
		t.Error("a refused rename renamed the project anyway")
	}
}
