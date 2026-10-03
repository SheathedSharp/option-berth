package groups

import (
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// Disk and naming are fixed while each batch is resolved. The expected policy
// is the pre-pass oracle, not the optimized code calling itself with a flag.
func TestResolutionPassMatchesReference(t *testing.T) {
	f := newFixture(t)
	nested := mkdir(t, f.repo, "backend", "nested")
	writeFile(t, filepath.Join(f.repo, ConfigName), "name: project\nports: [8000, 8001]\n")
	writeFile(t, filepath.Join(nested, ConfigName), "name: nested\nports: [8001, 8002]\n")
	writeFile(t, filepath.Join(f.worktree, ConfigName), "name: stale-project\nports: [8000, 8001]\n")
	seed := func() *Index {
		x := NewIndex()
		for _, dir := range []string{f.repo, nested, f.worktree} {
			x.Observe(dir)
		}
		x.SetAliases(map[string]string{f.repo: "renamed"})
		x.AddComposeProject("inside", f.repo)
		x.AddComposeProject("linked", f.worktree)
		x.AddComposeProject("outside", f.composeOut)
		return x
	}
	rng := rand.New(rand.NewSource(42))
	dirs := []string{"", f.repo, f.repoSub, nested, f.worktree, f.composeOut}
	projects := []string{"", "inside", "linked", "outside", "unknown"}
	runs := fakeRuns{8003: {group: "project", name: "api"}, 8004: {group: "explicit", name: "api"}, 8005: {group: "", name: "tag-only"}}
	for batch := 0; batch < 100; batch++ {
		input := make([]state.Port, 64)
		for i := range input {
			input[i] = nativePort(8000+rng.Intn(8), dirs[rng.Intn(len(dirs))])
			if project := projects[rng.Intn(len(projects))]; project != "" {
				input[i].Docker = &state.Docker{ComposeProject: project}
			}
			stale := "must-not-survive"
			input[i].Group, input[i].ProjectRoot = &stale, &stale
		}
		saved := append([]state.Port(nil), input...)
		actual := Resolve(input, runs, seed())
		want := append([]state.Port(nil), input...)
		refIndex := seed()
		for i := range want {
			referenceResolveOne(&want[i], runs, refIndex)
		}
		if !reflect.DeepEqual(actual, want) {
			t.Fatalf("batch=%d: resolution differs from reference", batch)
		}
		if !reflect.DeepEqual(input, saved) {
			t.Fatal("public Resolve mutated its input")
		}
	}
}

func TestResolutionPassDoesNotMemoizeRunHitsOrNames(t *testing.T) {
	c := newCheckouts(t, "project", "feature")
	x := NewIndex()
	r := newResolutionPass(x)
	p := nativePort(8000, c.wt)
	runs := fakeRuns{8000: {group: "project", name: "api"}}
	rows := []state.Port{p}
	r.resolve(rows, runs)
	if deref(rows[0].Group) != "project@feature" {
		t.Fatal(rows[0])
	}
	x.SetAliases(map[string]string{c.main: "renamed"})
	r.resolve(rows, runs)
	if deref(rows[0].Group) != "renamed@feature" {
		t.Fatal("alias relation was memoized")
	}
	delete(runs, 8000)
	r.resolve(rows, runs)
	if rows[0].GroupSource == nil || *rows[0].GroupSource == state.SourceStart {
		t.Fatal("run hit was memoized")
	}
}

func TestResolutionPassRechecksMovedAndRetargetedPaths(t *testing.T) {
	base := tempTree(t)
	a, b := mkdir(t, base, "a"), mkdir(t, base, "b")
	mkdir(t, a, ".git")
	mkdir(t, b, ".git")
	link := filepath.Join(base, "alias")
	if err := os.Symlink(a, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	x := NewIndex()
	first := newResolutionPass(x)
	got, ok := first.locate(link)
	if !ok || got.Root != a {
		t.Fatal(got, ok)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, link); err != nil {
		t.Fatal(err)
	}
	if same, _ := first.locate(link); same.Root != a {
		t.Fatal("within-pass observation changed")
	}
	if fresh, _ := newResolutionPass(x).locate(link); fresh.Root != b {
		t.Fatal("new pass kept old symlink target")
	}
}

func TestResolutionPassSharesAncestorsButRechecksNextBatch(t *testing.T) {
	base := tempTree(t)
	root := mkdir(t, base, "repo")
	mkdir(t, root, ".git")
	a, b := mkdir(t, root, "a"), mkdir(t, root, "b")
	x := NewIndex()
	first := newResolutionPass(x)
	first.observe(a)
	first.observe(b)
	if len(first.probed) != 3 {
		t.Fatalf("observations=%d, want 3 distinct directories", len(first.probed))
	}
	writeFile(t, filepath.Join(root, ConfigName), "name: discovered\nports: [8080]\n")
	pp := []ports.ListeningPort{{Port: 8080, Cwd: a}, {Port: 8080, Cwd: b}}
	got, _ := AttributeWith(pp, nil, x)
	for _, row := range got {
		if deref(row.Group) != "discovered" {
			t.Fatal("negative observation escaped its batch")
		}
	}
}

func TestResolutionPassKeepsDirectoryBoundaries(t *testing.T) {
	base := tempTree(t)
	root := mkdir(t, base, "project")
	sibling := mkdir(t, base, "project-copy")
	nested := mkdir(t, root, "nested")
	pass := newResolutionPass(NewIndex())
	for _, path := range []string{root, sibling, nested, "", filepath.Join(root, "future")} {
		if got, want := pass.under(path, root), Under(path, root); got != want {
			t.Fatalf("%q: got %v want %v", path, got, want)
		}
	}
}

// A missing/read-failed HEAD must not leave a reusable success; restoring it
// is observed immediately by the next call, independent of wall-clock ticks.
func TestBranchMissingAndRestored(t *testing.T) {
	c := newCheckouts(t, "project", "feature")
	co, _ := Locate(c.main)
	x := NewIndex()
	if got := x.Branch(co); got != "main" {
		t.Fatal(got)
	}
	path := filepath.Join(c.main, ".git", "HEAD")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := x.Branch(co); got != "" {
		t.Fatal("missing HEAD retained a branch")
	}
	writeFile(t, path, "ref: refs/heads/next\n")
	stamp := time.Unix(1, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if got := x.Branch(co); got != "next" {
		t.Fatal(got)
	}
}

func TestResolutionPassHeadLifetime(t *testing.T) {
	c := newCheckouts(t, "project", "feature")
	co, _ := Locate(c.wt)
	x := NewIndex()
	r := newResolutionPass(x)
	if got := r.branch(co); got != "feature/x" {
		t.Fatal(got)
	}
	writeFile(t, filepath.Join(c.admin, "HEAD"), "ref: refs/heads/feature/z\n")
	if got := r.branch(co); got != "feature/x" {
		t.Fatal("HEAD reread within one group publication")
	}
	if got := newResolutionPass(x).branch(co); got != "feature/z" {
		t.Fatal("HEAD observation outlived its pass")
	}
}
