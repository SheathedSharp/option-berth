package groups

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// gitProjects is a disposable parent with named checkouts. "base" is the
// parent itself, which must stay outside every repository boundary.
func gitProjects(t *testing.T, names ...string) map[string]string {
	t.Helper()
	base := tempTree(t)
	dirs := map[string]string{"base": base}
	for _, name := range names {
		dir := mkdir(t, base, name)
		mkdir(t, dir, ".git")
		dirs[name] = dir
	}
	return dirs
}

func writeManifests(t *testing.T, dirs map[string]string, files map[string]map[string]string) {
	t.Helper()
	for role, named := range files {
		dir, ok := dirs[role]
		if !ok {
			t.Fatalf("unknown layout role %q", role)
		}
		for name, body := range named {
			writeFile(t, filepath.Join(dir, name), body)
		}
	}
}

func observeRoles(t *testing.T, dirs map[string]string, roles ...string) *Index {
	t.Helper()
	x := NewIndex()
	for _, role := range roles {
		dir, ok := dirs[role]
		if !ok {
			t.Fatalf("unknown observe role %q", role)
		}
		x.Observe(dir)
	}
	return x
}

func assertManifestName(t *testing.T, cfg *Config, want string) {
	t.Helper()
	if want == "" {
		if cfg != nil {
			t.Fatalf("got %+v, want no manifest", cfg)
		}
		return
	}
	if cfg == nil || cfg.Name != want {
		t.Fatalf("got %+v, want name %q", cfg, want)
	}
}

func assertAtName(t *testing.T, x *Index, dir, want string) {
	t.Helper()
	assertManifestName(t, x.At(dir), want)
}

func assertNearestName(t *testing.T, x *Index, dir, want string) {
	t.Helper()
	assertManifestName(t, x.Nearest(dir), want)
}

// claimedPort is the caller-visible result of MatchPort: whether a unique
// declaration owns the port, which project it is, and which service declared
// it. A top-level ports: entry has an empty service.
type claimedPort struct {
	port    int
	cwd     string
	ok      bool
	name    string
	dir     string
	service string
}

func assertClaims(t *testing.T, x *Index, claims ...claimedPort) {
	t.Helper()
	for _, want := range claims {
		cfg, svc, ok := x.MatchPort(state.Port{Port: want.port, Cwd: want.cwd})
		gotName, gotDir := "", ""
		if cfg != nil {
			gotName, gotDir = cfg.Name, cfg.Dir
		}
		if ok != want.ok || gotName != want.name || svc != want.service || (want.dir != "" && gotDir != want.dir) {
			t.Fatalf("MatchPort(%d, %q) = name %q dir %q service %q ok=%v, want name %q dir %q service %q ok=%v",
				want.port, want.cwd, gotName, gotDir, svc, ok, want.name, want.dir, want.service, want.ok)
		}
	}
}

func observeNamedFiles(t *testing.T, files map[string]string) (string, *Index) {
	t.Helper()
	dirs := gitProjects(t, "repo")
	named := map[string]string{}
	for name, body := range files {
		named[name] = body
	}
	writeManifests(t, dirs, map[string]map[string]string{"repo": named})
	return dirs["repo"], observeRoles(t, dirs, "repo")
}

// TestIndexReprobesADirectoryAfterManifestAppears pins the invalidation rule
// from decision 0042: "no manifest here" is only valid until the directory's
// entries change. A manifest written into an already-observed directory must
// be found by the next observe, not stay invisible for the index's lifetime.
func TestIndexReprobesADirectoryAfterManifestAppears(t *testing.T) {
	dirs := gitProjects(t, "repo")
	deep := mkdir(t, dirs["repo"], "services", "api")
	dirs["deep"] = deep
	x := observeRoles(t, dirs, "deep")
	if len(x.Configs()) != 0 {
		t.Fatalf("configs before the manifest existed = %d, want 0", len(x.Configs()))
	}
	// Appearance is a new selected path and new bytes, not a timestamp tick.
	writeManifests(t, dirs, map[string]map[string]string{"repo": {ConfigName: "name: repo\n"}})
	x.Observe(deep)
	got := x.Configs()
	if len(got) != 1 || got[0].Name != "repo" {
		t.Fatalf("configs after the manifest = %+v, want the repo config", got)
	}
	assertNearestName(t, x, deep, "repo")
}

// Empty lists keep the shape their callers were written against: Configs and
// FilesIn are nil when there is nothing, while Invalid and Known are non-nil
// empty slices because they are rendered directly.
func TestEmptyIndexListsKeepTheirContract(t *testing.T) {
	x := NewIndex()
	if got := x.Configs(); got != nil {
		t.Errorf("Configs() = %#v, want nil", got)
	}
	if got := x.Invalid(); got == nil {
		t.Error("Invalid() = nil, want an empty slice")
	}
	if got := x.Known(); got == nil {
		t.Error("Known() = nil, want an empty slice")
	}
	if got := FilesIn(tempTree(t)); got != nil {
		t.Errorf("FilesIn(empty dir) = %#v, want nil", got)
	}
}

func TestIndexObserveWalksUpToTheGitRoot(t *testing.T) {
	dirs := gitProjects(t, "repo")
	dirs["deep"] = mkdir(t, dirs["repo"], "services", "api")
	writeManifests(t, dirs, map[string]map[string]string{
		"repo": {ConfigName: "name: repo\n"},
		"base": {ConfigName: "name: outer\n"},
	})
	x := observeRoles(t, dirs, "deep")
	assertNearestName(t, x, dirs["deep"], "repo")
	assertAtName(t, x, dirs["base"], "")
}

func TestIndexObserveOutsideARepoOnlyLooksAtTheDirectory(t *testing.T) {
	base := tempTree(t)
	dirs := map[string]string{"base": base, "loose": mkdir(t, base, "loose")}
	writeManifests(t, dirs, map[string]map[string]string{
		"loose": {ConfigName: "name: loose\n"},
		"base":  {ConfigName: "name: outer\n"},
	})
	x := observeRoles(t, dirs, "loose")
	assertAtName(t, x, dirs["loose"], "loose")
	assertAtName(t, x, dirs["base"], "")
}

func TestIndexDeepestConfigWins(t *testing.T) {
	dirs := gitProjects(t, "repo")
	dirs["inner"] = mkdir(t, dirs["repo"], "frontend")
	writeManifests(t, dirs, map[string]map[string]string{
		"repo":  {ConfigName: "name: outer\nservices:\n  - name: api\n    port: 8000\n"},
		"inner": {ConfigName: "name: inner\nservices:\n  - name: web\n    port: 8000\n"},
	})
	x := observeRoles(t, dirs, "inner")
	assertClaims(t, x,
		claimedPort{port: 8000, cwd: dirs["inner"], ok: true, name: "inner", service: "web"},
		claimedPort{port: 8000, cwd: dirs["repo"], ok: true, name: "outer", service: "api"},
		// Two configs claim 8000, and with no cwd there is nothing to tell them
		// apart: an ambiguous claim is not a group.
		claimedPort{port: 8000, ok: false},
		claimedPort{port: 9999, cwd: dirs["inner"], ok: false},
	)
}

// TestIndexMatchesADeclaredPortWithoutACwd is the Windows gap: the scanner has
// no per-process working directory there, so a listener arrives with Cwd
// empty. A `.oberth.yaml` that declares the port is still the only claim on it,
// and the group has to form — otherwise `oberth up` starts a group nothing ever
// joins.
func TestIndexMatchesADeclaredPortWithoutACwd(t *testing.T) {
	dirs := gitProjects(t, "repo")
	writeManifests(t, dirs, map[string]map[string]string{
		"repo": {ConfigName: "name: demo\nservices:\n  - name: api\n    port: 8000\nports: [8100]\n"},
	})
	x := observeRoles(t, dirs, "repo")
	assertClaims(t, x,
		claimedPort{port: 8000, ok: true, name: "demo", service: "api"},
		claimedPort{port: 8100, ok: true, name: "demo", service: ""},
		claimedPort{port: 9999, ok: false},
	)
	// The resolver has to reach the same conclusion, since that is what puts
	// the port in the group.
	got := Resolve([]state.Port{{Port: 8000}}, NoRuns{}, x)[0]
	if deref(got.Group) != "demo" {
		t.Errorf("resolved group = %q, want demo", deref(got.Group))
	}
	if got.GroupSource == nil || *got.GroupSource != state.SourceFile {
		t.Errorf("group source = %v, want file", got.GroupSource)
	}
}

func TestIndexClaimLookupRefreshesAfterConfigChanges(t *testing.T) {
	// These directories are in-memory keys, not files created on the host.
	x := NewIndex()
	x.Add(&Config{Dir: "/tmp/one", Services: []Service{{Name: "api", Port: 8000}}})
	assertClaims(t, x, claimedPort{port: 8000, ok: true, dir: "/tmp/one", service: "api"})

	x.Add(&Config{Dir: "/tmp/two", Services: []Service{{Name: "web", Port: 8100}}})
	assertClaims(t, x, claimedPort{port: 8100, ok: true, dir: "/tmp/two", service: "web"})

	x.Add(&Config{Dir: "/tmp/one", Services: []Service{{Name: "api", Port: 8200}}})
	assertClaims(t, x,
		claimedPort{port: 8000, ok: false},
		claimedPort{port: 8200, ok: true, dir: "/tmp/one", service: "api"},
	)
}

// TestIndexWillNotGuessBetweenTwoConfigsClaimingAPort: without a cwd, two
// projects declaring the same port are indistinguishable, and a wrong group is
// worse than none.
func TestIndexWillNotGuessBetweenTwoConfigsClaimingAPort(t *testing.T) {
	dirs := gitProjects(t, "one", "two")
	writeManifests(t, dirs, map[string]map[string]string{
		"one": {ConfigName: "name: one\nservices:\n  - name: api\n    port: 8000\n"},
		"two": {ConfigName: "name: two\nservices:\n  - name: api\n    port: 8000\n"},
	})
	x := observeRoles(t, dirs, "one", "two")
	assertClaims(t, x,
		claimedPort{port: 8000, ok: false},
		claimedPort{port: 8000, cwd: dirs["two"], ok: true, name: "two", service: "api"},
	)
}

func TestIndexRecordsInvalidConfigsWithoutFailing(t *testing.T) {
	dirs := gitProjects(t, "repo")
	writeManifests(t, dirs, map[string]map[string]string{"repo": {ConfigName: "name: a b\n"}})
	x := observeRoles(t, dirs, "repo")
	if errs := x.Invalid(); len(errs) != 1 || !strings.Contains(errs[0].Err.Error(), "whitespace") {
		t.Fatalf("Invalid = %+v, want one whitespace complaint", errs)
	}
	if cfgs := x.Configs(); len(cfgs) != 0 {
		t.Fatalf("Configs = %v, want none", cfgs)
	}
	// Resolving still works: the group falls back to the git root name.
	got := Resolve([]state.Port{nativePort(8123, dirs["repo"])}, NoRuns{}, x)[0]
	if deref(got.Group) != "repo" {
		t.Fatalf("group = %q, want the git root name", deref(got.Group))
	}
}

func TestIndexAcceptsTheYmlSpelling(t *testing.T) {
	dir, x := observeNamedFiles(t, map[string]string{".oberth.yml": "name: ymlrepo\n"})
	assertAtName(t, x, dir, "ymlrepo")
}

// TestIndexReadsTheOldDotfile: projects committed before the rename keep
// working, and so do the worktrees that carry a copy of their file.
func TestIndexReadsTheOldDotfile(t *testing.T) {
	dir, x := observeNamedFiles(t, map[string]string{LegacyConfigName: "name: legacy\n"})
	assertAtName(t, x, dir, "legacy")
}

// TestIndexPrefersTheCurrentName: with both files in one directory, oberth.yaml
// is the one read and the dotfile is shadowed, never merged.
func TestIndexPrefersTheCurrentName(t *testing.T) {
	dir, x := observeNamedFiles(t, map[string]string{
		LegacyConfigName: "name: old\n",
		ConfigName:       "name: new\n",
	})
	assertAtName(t, x, dir, "new")
	present := FilesIn(dir)
	if len(present) != 2 || filepath.Base(present[0]) != ConfigName || filepath.Base(present[1]) != LegacyConfigName {
		t.Errorf("FilesIn = %v, want oberth.yaml then .oberth.yaml", present)
	}
}

// TestTargetIn: a write goes to the file already there, whatever its spelling,
// and only a directory without one gets a new oberth.yaml.
func TestTargetIn(t *testing.T) {
	base := tempTree(t)
	fresh := mkdir(t, base, "fresh")
	if want := filepath.Join(fresh, ConfigName); TargetIn(fresh) != want {
		t.Errorf("TargetIn(fresh) = %s, want a new %s", TargetIn(fresh), want)
	}
	old := mkdir(t, base, "old")
	writeFile(t, filepath.Join(old, LegacyConfigName), "name: legacy\n")
	if want := filepath.Join(old, LegacyConfigName); TargetIn(old) != want {
		t.Errorf("TargetIn(old) = %s, want the existing %s", TargetIn(old), want)
	}
}

func TestIsLegacyName(t *testing.T) {
	for _, tc := range []struct {
		name   string
		legacy bool
	}{
		{".oberth.yaml", true},
		{".oberth.yml", true},
		{"oberth.yaml", false},
		{"oberth.yml", false},
		{".env", false},
	} {
		if got := IsLegacyName(tc.name); got != tc.legacy {
			t.Errorf("IsLegacyName(%q) = %v, want %v", tc.name, got, tc.legacy)
		}
	}
}

// TestIndexKeysConfigsCanonically: however a config's path was spelled — a
// Reload root handed back by the store, or a path a client typed — it has to
// land under the same key as that directory found by walking a process cwd.
// On macOS the two spellings differ for everything under $TMPDIR (/var against
// /private/var), and a mismatch hides the config from Nearest, leaving `oberth
// start` to fall back to the git root or the directory name.
func TestIndexKeysConfigsCanonically(t *testing.T) {
	realDir := tempTree(t)
	project := mkdir(t, realDir, "repo")
	writeFile(t, filepath.Join(project, ConfigName), "name: my-app\n")

	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	viaAlias := filepath.Join(alias, "repo")

	t.Run("AddFile", func(t *testing.T) {
		x := NewIndex()
		if err := x.AddFile(filepath.Join(viaAlias, ConfigName)); err != nil {
			t.Fatal(err)
		}
		assertNearestName(t, x, project, "my-app")
		assertAtName(t, x, viaAlias, "my-app")
	})

	t.Run("Reload", func(t *testing.T) {
		x := NewIndex()
		if n, bad := x.Reload([]string{viaAlias}); n != 1 || len(bad) != 0 {
			t.Fatalf("Reload = %d, %v", n, bad)
		}
		assertNearestName(t, x, filepath.Join(project, "sub", "dir"), "my-app")
	})

	t.Run("ByPath", func(t *testing.T) {
		x := NewIndex()
		x.Observe(project)
		if cfg, ok := x.ByPath(filepath.Join(viaAlias, ConfigName)); !ok || cfg.Name != "my-app" {
			t.Fatalf("ByPath through a symlink = %+v, %v", cfg, ok)
		}
	})
}
