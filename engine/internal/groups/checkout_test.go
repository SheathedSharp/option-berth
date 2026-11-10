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

func TestLocate(t *testing.T) {
	c := newCheckouts(t, "shop", "feat")
	deep := mkdir(t, c.wt, "web", "src")

	co, ok := Locate(mkdir(t, c.main, "api"))
	if !ok || co.Root != c.main || co.Main != c.main || co.Linked() {
		t.Fatalf("main checkout = %+v, %v", co, ok)
	}

	co, ok = Locate(deep)
	if !ok || co.Root != c.wt || co.Main != c.main || co.Worktree != "feat" || !co.Linked() {
		t.Fatalf("worktree = %+v, %v", co, ok)
	}

	// A relative gitdir resolves against the worktree root.
	rel := mkdir(t, filepath.Dir(c.wt), "relwt")
	target, err := filepath.Rel(rel, filepath.Join(c.main, ".git", "worktrees", "relwt"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(rel, ".git"), "gitdir: "+target+"\n")
	if co, ok := Locate(rel); !ok || co.Main != c.main || co.Worktree != "relwt" {
		t.Fatalf("relative worktree = %+v, %v", co, ok)
	}

	// A submodule's .git file points into .git/modules: it is its own main
	// checkout, not a worktree of the superproject.
	sub := mkdir(t, c.main, "vendor", "lib")
	writeFile(t, filepath.Join(sub, ".git"), "gitdir: ../../.git/modules/lib\n")
	if co, ok := Locate(sub); !ok || co.Root != sub || co.Main != sub || co.Linked() {
		t.Fatalf("submodule = %+v, %v", co, ok)
	}

	if _, ok := Locate(tempTree(t)); ok {
		t.Fatal("found a checkout outside any repository")
	}
	if _, ok := Locate(""); ok {
		t.Fatal("found a checkout for an empty path")
	}
}

// TestLocateAcceptsEverySpellingOfTheWorktree is the worktree half of the
// Windows spellings test: with a trailing separator or a redundant `.`, a
// worktree still names one root and one main checkout.
func TestLocateAcceptsEverySpellingOfTheWorktree(t *testing.T) {
	c := newCheckouts(t, "shop", "feat")
	sep := string(filepath.Separator)
	for _, dir := range []string{c.wt, c.wt + sep, filepath.Join(c.wt, "."), filepath.Join(c.wt, "x", "..")} {
		co, ok := Locate(dir)
		if !ok || co.Root != c.wt || co.Main != c.main || co.Worktree != "feat" {
			t.Errorf("Locate(%q) = %+v, %v", dir, co, ok)
		}
	}
}

func TestBranch(t *testing.T) {
	c := newCheckouts(t, "shop", "feat")
	x := NewIndex()
	main, _ := Locate(c.main)
	wt, _ := Locate(c.wt)

	if got := x.Branch(main); got != "main" {
		t.Errorf("main branch = %q", got)
	}
	if got := x.Branch(wt); got != "feature/x" {
		t.Errorf("worktree branch = %q, want the HEAD in the main .git/worktrees/feat", got)
	}

	// A checkout switch rewrites HEAD; the cache notices the new size.
	writeFile(t, filepath.Join(c.admin, "HEAD"), "ref: refs/heads/fix\n")
	if got := x.Branch(wt); got != "fix" {
		t.Errorf("after switching, worktree branch = %q", got)
	}
	// Detached.
	writeFile(t, filepath.Join(c.admin, "HEAD"), "4b825dc642cb6eb9a060e54bf8d69288fbee4904\n")
	if got := x.Branch(wt); got != "" {
		t.Errorf("detached HEAD branch = %q, want empty", got)
	}

	// A checkout with no readable HEAD, like the resolver fixtures.
	bare := mkdir(t, tempTree(t), "nohead")
	mkdir(t, bare, ".git")
	co, _ := Locate(bare)
	if got := x.Branch(co); got != "" {
		t.Errorf("branch without HEAD = %q", got)
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
			mainConfig: manifest("shop", 8000), linkedConfig: manifest("shop", 8000), expected: []namingExpectation{
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
			x.Observe(c.wt)
			// The same-name case used to observe both roots explicitly. Keep that
			// discovery input, rather than relying on a lucky main-root probe.
			if scenario.title == "a copy with the same name as main is still its own group" {
				x.Observe(c.main)
			}
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

// TestRunGroupFollowsTheCheckout: a run recorded under the project's own name
// joins its checkout's group; one started with a --group of its own keeps it.
func TestRunGroupFollowsTheCheckout(t *testing.T) {
	c := newCheckouts(t, "app", "feat")
	x := NewIndex()
	x.Observe(c.wt)
	runs := fakeRuns{
		5000: {group: "app", name: "web"},      // a pre-5A.6 client in the worktree
		5001: {group: "app@feat", name: "api"}, // the current spelling
		5002: {group: "custom", name: "job"},   // --group custom
		5003: {group: "app", name: "web"},      // in the main checkout
	}
	pp := Resolve([]state.Port{
		nativePort(5000, c.wt), nativePort(5001, c.wt), nativePort(5002, c.wt), nativePort(5003, c.main),
	}, runs, x)
	want := []string{"app@feat", "app@feat", "custom", "app"}
	for i, p := range pp {
		if deref(p.Group) != want[i] {
			t.Errorf("port %d group = %q, want %q", p.Port, deref(p.Group), want[i])
		}
	}

	// After an alias, both spellings of the old name move with the project.
	x.SetAliases(map[string]string{c.main: "store"})
	pp = Resolve([]state.Port{nativePort(5001, c.wt), nativePort(5003, c.main)}, runs, x)
	if deref(pp[0].Group) != "store@feat" || deref(pp[1].Group) != "store" {
		t.Errorf("aliased run groups = %q, %q", deref(pp[0].Group), deref(pp[1].Group))
	}
}

// TestGroupsPublishEachCheckoutOnce is the groups.list half: the main checkout
// and the worktree are two groups with distinct names — even stopped ones
// known only from the index — and each says which project, worktree and branch
// it is.
func TestGroupsPublishEachCheckoutOnce(t *testing.T) {
	c := newCheckouts(t, "shop-dir", "feat")
	writeFile(t, filepath.Join(c.main, ConfigName), "name: shop\nservices:\n  - name: api\n    port: 8000\n")
	writeFile(t, filepath.Join(c.wt, ConfigName), "name: shop\nservices:\n  - name: api\n    port: 8000\n")

	for _, running := range []bool{false, true} {
		x := NewIndex()
		x.Observe(c.wt)
		var pp []state.Port
		if running {
			pp = Resolve([]state.Port{nativePort(8000, c.wt)}, NoRuns{}, x)
		}
		gg := Groups(pp, x)

		byName := map[string]state.Group{}
		for _, g := range gg {
			if _, dup := byName[g.Name]; dup {
				t.Fatalf("running=%v: two groups named %q in %+v", running, g.Name, gg)
			}
			byName[g.Name] = g
		}
		main, ok := byName["shop"]
		if !ok {
			t.Fatalf("running=%v: no main group in %v", running, groupNames(gg))
		}
		wt, ok := byName["shop@feat"]
		if !ok {
			t.Fatalf("running=%v: no worktree group in %v", running, groupNames(gg))
		}
		if main.Repo != "shop" || main.Worktree != "" || main.Branch != "main" {
			t.Errorf("main checkout fields = repo %q worktree %q branch %q", main.Repo, main.Worktree, main.Branch)
		}
		if wt.Repo != "shop" || wt.Worktree != "feat" || wt.Branch != "feature/x" {
			t.Errorf("worktree fields = repo %q worktree %q branch %q", wt.Repo, wt.Worktree, wt.Branch)
		}
		if wt.ConfigPath == nil || *wt.ConfigPath != filepath.Join(c.wt, ConfigName) {
			t.Errorf("worktree config_path = %v, want its own copy", wt.ConfigPath)
		}
		if main.Identity != WorktreeTokenFor(c.main) || wt.Identity != WorktreeTokenFor(c.wt) {
			t.Errorf("identity = %q / %q, want %q / %q",
				main.Identity, wt.Identity, WorktreeTokenFor(c.main), WorktreeTokenFor(c.wt))
		}
		if running && (len(wt.Services) != 1 || !wt.Services[0].Running || len(main.Members) != 0) {
			t.Errorf("the worktree's api is the worktree's: main %+v, worktree %+v", main, wt)
		}
	}
}

func TestGroupsFieldsForAGroupThatIsNotACheckout(t *testing.T) {
	gg := Groups(Resolve([]state.Port{composePort(5433, "loose", "db")}, NoRuns{}, NewIndex()), NewIndex())
	if len(gg) != 1 || gg[0].Repo != "loose" || gg[0].Worktree != "" || gg[0].Branch != "" {
		t.Fatalf("groups = %+v", gg)
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
}

func groupNames(gg []state.Group) []string {
	out := make([]string, 0, len(gg))
	for _, g := range gg {
		out = append(out, g.Name)
	}
	return out
}
