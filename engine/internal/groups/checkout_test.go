package groups

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// checkouts is a main checkout with a real-looking .git (HEAD on main) and one
// linked worktree whose admin directory holds its own HEAD.
type checkouts struct {
	main, wt, admin string
}

func newCheckouts(t *testing.T, mainName, wtName string) checkouts {
	t.Helper()
	base := tempTree(t)
	main := mkdir(t, base, mainName)
	writeFile(t, filepath.Join(main, ".git", "HEAD"), "ref: refs/heads/main\n")
	admin := filepath.Join(main, ".git", "worktrees", wtName)
	writeFile(t, filepath.Join(admin, "HEAD"), "ref: refs/heads/feature/x\n")
	wt := mkdir(t, base, "wt", wtName)
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+admin+"\n")
	return checkouts{main: main, wt: wt, admin: admin}
}

type checkoutWant struct {
	root, main, worktree string
	linked               bool
}

func requireLocate(t *testing.T, dir string, want checkoutWant) {
	t.Helper()
	co, ok := Locate(dir)
	if !ok {
		t.Fatalf("Locate(%q) found no checkout", dir)
	}
	if co.Root != want.root || co.Main != want.main || co.Worktree != want.worktree || co.Linked() != want.linked {
		t.Errorf("Locate(%q) = %+v linked=%v, want root %q main %q worktree %q linked %v",
			dir, co, co.Linked(), want.root, want.main, want.worktree, want.linked)
	}
}

func requireNoCheckout(t *testing.T, dir string) {
	t.Helper()
	co, ok := Locate(dir)
	if ok || co != (Checkout{}) {
		t.Errorf("Locate(%q) = %+v, %v; want no checkout", dir, co, ok)
	}
}

func TestLocate(t *testing.T) {
	c := newCheckouts(t, "shop", "feat")
	deep := mkdir(t, c.wt, "web", "src")

	requireLocate(t, mkdir(t, c.main, "api"), checkoutWant{root: c.main, main: c.main})
	requireLocate(t, deep, checkoutWant{root: c.wt, main: c.main, worktree: "feat", linked: true})

	rel := mkdir(t, filepath.Dir(c.wt), "relwt")
	target, err := filepath.Rel(rel, filepath.Join(c.main, ".git", "worktrees", "relwt"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(rel, ".git"), "gitdir: "+target+"\n")
	requireLocate(t, rel, checkoutWant{root: rel, main: c.main, worktree: "relwt", linked: true})

	sub := mkdir(t, c.main, "vendor", "lib")
	writeFile(t, filepath.Join(sub, ".git"), "gitdir: ../../.git/modules/lib\n")
	requireLocate(t, sub, checkoutWant{root: sub, main: sub})

	requireNoCheckout(t, tempTree(t))
	requireNoCheckout(t, "")
}

func TestLocateAcceptsEverySpellingOfTheWorktree(t *testing.T) {
	c := newCheckouts(t, "shop", "feat")
	sep := string(filepath.Separator)
	want := checkoutWant{root: c.wt, main: c.main, worktree: "feat", linked: true}
	for _, dir := range []string{c.wt, c.wt + sep, filepath.Join(c.wt, "."), filepath.Join(c.wt, "x", "..")} {
		requireLocate(t, dir, want)
	}
}

func TestBranch(t *testing.T) {
	c := newCheckouts(t, "shop", "feat")
	x := NewIndex()
	main, ok := Locate(c.main)
	if !ok {
		t.Fatal("Locate(main) found no checkout")
	}
	wt, ok := Locate(c.wt)
	if !ok {
		t.Fatal("Locate(worktree) found no checkout")
	}

	if got := x.Branch(main); got != "main" {
		t.Errorf("main branch = %q, want main", got)
	}
	if got := x.Branch(wt); got != "feature/x" {
		t.Errorf("worktree branch = %q, want feature/x from the admin HEAD", got)
	}

	writeFile(t, filepath.Join(c.admin, "HEAD"), "ref: refs/heads/fix\n")
	if got := x.Branch(wt); got != "fix" {
		t.Errorf("after switching, worktree branch = %q, want fix", got)
	}
	// A same-length replacement must still be visible: length and mtime are not the contract.
	writeFile(t, filepath.Join(c.admin, "HEAD"), "ref: refs/heads/dev\n")
	if got := x.Branch(wt); got != "dev" {
		t.Errorf("after a same-length switch, worktree branch = %q, want dev", got)
	}

	writeFile(t, filepath.Join(c.admin, "HEAD"), "4b825dc642cb6eb9a060e54bf8d69288fbee4904\n")
	if got := x.Branch(wt); got != "" {
		t.Errorf("detached HEAD branch = %q, want empty", got)
	}

	bare := mkdir(t, tempTree(t), "nohead")
	mkdir(t, bare, ".git")
	co, ok := Locate(bare)
	if !ok {
		t.Fatal("Locate(nohead) found no checkout")
	}
	if got := x.Branch(co); got != "" {
		t.Errorf("branch without HEAD = %q, want empty", got)
	}
}

func TestParseHead(t *testing.T) {
	for in, want := range map[string]string{
		"ref: refs/heads/main\n":       "main",
		"ref:refs/heads/feature/a-b":   "feature/a-b",
		"ref: refs/remotes/origin/x\n": "",
		"0123456789abcdef\n":           "",
		"":                             "",
	} {
		if got := parseHead([]byte(in)); got != want {
			t.Errorf("parseHead(%q) = %q, want %q", in, got, want)
		}
	}
}

func resolveOneAt(t *testing.T, x *Index, p state.Port) (string, state.GroupSource) {
	t.Helper()
	got := Resolve([]state.Port{p}, NoRuns{}, x)[0]
	var source state.GroupSource
	if got.GroupSource != nil {
		source = *got.GroupSource
	}
	return deref(got.Group), source
}

// TestWorktreeNaming: every linked worktree is
// `<project>@<worktree>`, and only the main checkout decides the project.
// namingExpectation describes one resolver observation; an empty source leaves
// source unconstrained only where the previous test checked the name alone.
type namingExpectation struct {
	checkout string
	port     int
	group    string
	source   state.GroupSource
}

func checkNaming(t *testing.T, x *Index, directories map[string]string, expected []namingExpectation) {
	t.Helper()
	for _, want := range expected {
		got, source := resolveOneAt(t, x, nativePort(want.port, directories[want.checkout]))
		if got != want.group {
			t.Errorf("%s:%d group = %q, want %q", want.checkout, want.port, got, want.group)
		}
		if want.source != "" && source != want.source {
			t.Errorf("%s:%d source = %s, want %s", want.checkout, want.port, source, want.source)
		}
	}
}

func TestWorktreeNaming(t *testing.T) {
	manifest := func(name string, port int) string {
		return fmt.Sprintf("name: %s\nservices:\n  - name: api\n    port: %d\n", name, port)
	}
	cases := []struct {
		title, directory, mainConfig, linkedConfig string
		observeMain                                bool
		expected                                   []namingExpectation
	}{
		{title: "no config anywhere", directory: "app", expected: []namingExpectation{
			{"main", 3000, "app", state.SourceAuto}, {"linked", 3001, "app@feat", state.SourceAuto},
		}},
		{title: "a worktree's stale copy of the config never names it", directory: "shop-dir",
			mainConfig: manifest("shop", 8000), linkedConfig: manifest("old-shop", 8100), expected: []namingExpectation{
				{"main", 8000, "shop", state.SourceFile}, {"linked", 8100, "shop@feat", state.SourceFile},
				{"linked", 9999, "shop@feat", state.SourceFile},
			}},
		{title: "a copy with the same name as main is still its own group", directory: "shop",
			mainConfig: manifest("shop", 8000), linkedConfig: manifest("shop", 8000), observeMain: true, expected: []namingExpectation{
				{"main", 8000, "shop", ""}, {"linked", 8000, "shop@feat", ""},
			}},
	}
	for _, scenario := range cases {
		t.Run(scenario.title, func(t *testing.T) {
			c := newCheckouts(t, scenario.directory, "feat")
			directories := map[string]string{"main": c.main, "linked": c.wt}
			for role, body := range map[string]string{"main": scenario.mainConfig, "linked": scenario.linkedConfig} {
				if body != "" {
					writeFile(t, filepath.Join(directories[role], ConfigName), body)
				}
			}
			x := NewIndex()
			// Preserve the scenario's discovery order, independently of its
			// human-readable title: main first only for the same-name copy.
			if scenario.observeMain {
				x.Observe(c.main)
			}
			x.Observe(c.wt)
			checkNaming(t, x, directories, scenario.expected)
		})
	}
	t.Run("a worktree kept inside the main checkout", func(t *testing.T) {
		main := mkdir(t, tempTree(t), "shop")
		admin := mkdir(t, main, ".git", "worktrees", "feat")
		wt := mkdir(t, main, ".claude", "worktrees", "feat")
		writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+admin+"\n")
		writeFile(t, filepath.Join(main, ConfigName), "name: shop\nports: [3000]\n")
		x := NewIndex()
		x.Observe(main)
		x.Observe(wt)
		checkNaming(t, x, map[string]string{"main": main, "linked": wt}, []namingExpectation{
			{"linked", 3000, "shop@feat", state.SourceAuto}, {"main", 3000, "shop", ""},
		})
	})
	t.Run("an alias names the project and every worktree", func(t *testing.T) {
		c := newCheckouts(t, "app", "feat")
		x := NewIndex()
		x.Observe(c.wt)
		directories := map[string]string{"main": c.main, "linked": c.wt}
		x.SetAliases(map[string]string{c.main: "store"})
		checkNaming(t, x, directories, []namingExpectation{{"main", 3000, "store", ""}, {"linked", 3001, "store@feat", ""}})
		x.SetAliases(nil)
		checkNaming(t, x, directories, []namingExpectation{{"linked", 3001, "app@feat", ""}})
	})
	t.Run("submodules keep their own name", func(t *testing.T) {
		c := newCheckouts(t, "shop", "feat")
		sub := mkdir(t, c.main, "vendor", "lib")
		writeFile(t, filepath.Join(sub, ".git"), "gitdir: ../../.git/modules/lib\n")
		x := NewIndex()
		x.Observe(sub)
		checkNaming(t, x, map[string]string{"submodule": sub}, []namingExpectation{{"submodule", 4000, "lib", ""}})
	})
}

func TestRunGroupFollowsTheCheckout(t *testing.T) {
	c := newCheckouts(t, "app", "feat")
	x := NewIndex()
	x.Observe(c.wt)
	runs := fakeRuns{
		5000: {group: "app", name: "web"},
		5001: {group: "app@feat", name: "api"},
		5002: {group: "custom", name: "job"},
		5003: {group: "app", name: "web"},
	}

	type followWant struct {
		port   int
		cwd    string
		group  string
		root   string
		source state.GroupSource
	}
	assertFollow := func(t *testing.T, ports []state.Port, expected []followWant) {
		t.Helper()
		got := Resolve(ports, runs, x)
		if len(got) != len(expected) {
			t.Fatalf("Resolve returned %d ports, want %d", len(got), len(expected))
		}
		for i, want := range expected {
			g, src, root := deref(got[i].Group), state.GroupSource(""), deref(got[i].ProjectRoot)
			if got[i].GroupSource != nil {
				src = *got[i].GroupSource
			}
			if g != want.group || src != want.source || root != want.root {
				t.Errorf("port %d group=%q source=%s root=%q, want %q %s %q",
					want.port, g, src, root, want.group, want.source, want.root)
			}
		}
	}

	assertFollow(t, []state.Port{
		nativePort(5000, c.wt), nativePort(5001, c.wt), nativePort(5002, c.wt), nativePort(5003, c.main),
	}, []followWant{
		{5000, c.wt, "app@feat", c.wt, state.SourceStart},
		{5001, c.wt, "app@feat", c.wt, state.SourceStart},
		{5002, c.wt, "custom", c.wt, state.SourceStart},
		{5003, c.main, "app", c.main, state.SourceStart},
	})

	x.SetAliases(map[string]string{c.main: "store"})
	assertFollow(t, []state.Port{nativePort(5001, c.wt), nativePort(5003, c.main)}, []followWant{
		{5001, c.wt, "store@feat", c.wt, state.SourceStart},
		{5003, c.main, "store", c.main, state.SourceStart},
	})
}

func groupsByName(t *testing.T, gg []state.Group) map[string]state.Group {
	t.Helper()
	byName := map[string]state.Group{}
	for _, g := range gg {
		if _, dup := byName[g.Name]; dup {
			t.Fatalf("two groups named %q in %v", g.Name, groupNames(gg))
		}
		byName[g.Name] = g
	}
	return byName
}

func requireCheckoutGroup(t *testing.T, g state.Group, name, repo, worktree, branch, root, config, token string, running bool) {
	t.Helper()
	if g.Name != name || g.Repo != repo || g.Worktree != worktree || g.Branch != branch {
		t.Errorf("%s fields = name %q repo %q worktree %q branch %q, want %q / %q / %q / %q",
			name, g.Name, g.Repo, g.Worktree, g.Branch, name, repo, worktree, branch)
	}
	if g.Identity != token {
		t.Errorf("%s identity = %q, want %q", name, g.Identity, token)
	}
	if g.Source != state.SourceFile {
		t.Errorf("%s source = %s, want file", name, g.Source)
	}
	if g.RootDir == nil || *g.RootDir != root {
		t.Errorf("%s root_dir = %v, want %s", name, g.RootDir, root)
	}
	if g.ConfigPath == nil || *g.ConfigPath != config {
		t.Errorf("%s config_path = %v, want %s", name, g.ConfigPath, config)
	}
	wantStatus := "stopped"
	if running {
		wantStatus = "running"
	}
	if g.Status != wantStatus {
		t.Errorf("%s status = %q, want %q", name, g.Status, wantStatus)
	}
	if running {
		if len(g.Members) != 1 || g.Members[0] != 8000 || g.RunningServices != 1 {
			t.Errorf("%s members/running = %v / %d, want [8000] / 1", name, g.Members, g.RunningServices)
		}
	} else if len(g.Members) != 0 || g.RunningServices != 0 {
		t.Errorf("%s stopped members/running = %v / %d, want empty / 0", name, g.Members, g.RunningServices)
	}
	if len(g.Services) != 1 || g.Services[0].Name != "api" || g.Services[0].Running != running {
		t.Errorf("%s services = %+v, want api running=%v", name, g.Services, running)
	}
}

func TestGroupsPublishEachCheckoutOnce(t *testing.T) {
	c := newCheckouts(t, "shop-dir", "feat")
	writeFile(t, filepath.Join(c.main, ConfigName), "name: shop\nservices:\n  - name: api\n    port: 8000\n")
	writeFile(t, filepath.Join(c.wt, ConfigName), "name: shop\nservices:\n  - name: api\n    port: 8000\n")

	for _, running := range []bool{false, true} {
		t.Run(fmt.Sprintf("running=%v", running), func(t *testing.T) {
			x := NewIndex()
			x.Observe(c.wt)
			var pp []state.Port
			if running {
				pp = Resolve([]state.Port{nativePort(8000, c.wt)}, NoRuns{}, x)
			}
			byName := groupsByName(t, Groups(pp, x))
			main, ok := byName["shop"]
			if !ok {
				t.Fatalf("no main group in %v", groupNames(mapValues(byName)))
			}
			wt, ok := byName["shop@feat"]
			if !ok {
				t.Fatalf("no worktree group in %v", groupNames(mapValues(byName)))
			}
			requireCheckoutGroup(t, main, "shop", "shop", "", "main", c.main, filepath.Join(c.main, ConfigName), WorktreeTokenFor(c.main), false)
			requireCheckoutGroup(t, wt, "shop@feat", "shop", "feat", "feature/x", c.wt, filepath.Join(c.wt, ConfigName), WorktreeTokenFor(c.wt), running)
		})
	}
}

func mapValues(byName map[string]state.Group) []state.Group {
	out := make([]state.Group, 0, len(byName))
	for _, g := range byName {
		out = append(out, g)
	}
	return out
}

func TestGroupsFieldsForAGroupThatIsNotACheckout(t *testing.T) {
	gg := Groups(Resolve([]state.Port{composePort(5433, "loose", "db")}, NoRuns{}, NewIndex()), NewIndex())
	if len(gg) != 1 {
		t.Fatalf("len(groups) = %d, want 1", len(gg))
	}
	g := gg[0]
	if g.Name != "loose" || g.Repo != "loose" || g.Worktree != "" || g.Branch != "" || g.Identity != "" {
		t.Errorf("non-checkout group = %+v, want name and repo loose with empty worktree/branch/identity", g)
	}
	if g.Source != state.SourceAuto {
		t.Errorf("source = %s, want auto", g.Source)
	}
	if g.Status != "running" {
		t.Errorf("status = %q, want running", g.Status)
	}
	if g.RootDir != nil || g.ConfigPath != nil {
		t.Errorf("root_dir/config_path = %v / %v, want nil", g.RootDir, g.ConfigPath)
	}
}

func TestNamedMatchesTheGroupNotTheFile(t *testing.T) {
	c := newCheckouts(t, "shop", "feat")
	writeFile(t, filepath.Join(c.main, ConfigName), "name: shop\n")
	writeFile(t, filepath.Join(c.wt, ConfigName), "name: shop\n")
	x := NewIndex()
	x.Observe(c.wt)

	if cfg, ok := x.Named("shop"); !ok || cfg.Dir != c.main {
		t.Errorf("Named(shop) = %+v, %v; want the main checkout's file", cfg, ok)
	}
	if cfg, ok := x.Named("shop@feat"); !ok || cfg.Dir != c.wt {
		t.Errorf("Named(shop@feat) = %+v, %v; want the worktree's copy", cfg, ok)
	}
	if cfg, ok := x.Named(""); ok {
		t.Errorf("Named(\"\") = %+v, %v; want not found", cfg, ok)
	}
	if cfg, ok := x.Named("no-such"); ok {
		t.Errorf("Named(no-such) = %+v, %v; want not found", cfg, ok)
	}
}

func groupNames(gg []state.Group) []string {
	out := make([]string, 0, len(gg))
	for _, g := range gg {
		out = append(out, g.Name)
	}
	return out
}
