package groups

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestResolveDropsStaleProjectRoot(t *testing.T) {
	base := tempTree(t)
	repo := mkdir(t, base, "project")
	git := mkdir(t, repo, ".git")
	input := Resolve([]state.Port{nativePort(8080, repo)}, nil, nil)
	if deref(input[0].ProjectRoot) != repo {
		t.Fatal("initial checkout missing")
	}
	if err := os.Remove(git); err != nil {
		t.Fatal(err)
	}
	result := Resolve(input, nil, nil)
	if result[0].ProjectRoot != nil || result[0].Group != nil || result[0].GroupSource != nil {
		t.Fatalf("removed checkout survived: %+v", result[0])
	}
	if deref(input[0].ProjectRoot) != repo {
		t.Fatal("resolver mutated previous snapshot")
	}
	input[0].Cwd = ""
	if result := Resolve(input, nil, nil); result[0].ProjectRoot != nil {
		t.Fatal("missing cwd retained previous root")
	}
}

func TestComposeDirectoryConstrainsManifestClaims(t *testing.T) {
	base := tempTree(t)
	own := mkdir(t, base, "own")
	foreign := mkdir(t, base, "foreign")
	mkdir(t, own, ".git")
	mkdir(t, foreign, ".git")
	writeFile(t, filepath.Join(foreign, ConfigName), "name: foreign\nservices:\n  - name: api\n    port: 8080\n")
	index := NewIndex()
	index.Observe(foreign)
	index.AddComposeProject("stack", own)
	docker := composePort(8080, "stack", "api")
	result := Resolve([]state.Port{docker}, nil, index)[0]
	if deref(result.Group) != "own" || deref(result.ProjectRoot) != own {
		t.Fatalf("foreign manifest stole known Compose checkout: %+v", result)
	}
	if cfg, _, ok := index.MatchPort(docker); ok {
		t.Fatalf("foreign claim=%+v", cfg)
	}
	// No evidence still uses the existing unique-manifest rule for native rows.
	native := Resolve([]state.Port{nativePort(8080, "")}, nil, index)[0]
	if deref(native.Group) != "foreign" {
		t.Fatalf("native unknown rule changed: %+v", native)
	}
	// Once a local declaration appears, file precedence is retained.
	writeFile(t, filepath.Join(own, ConfigName), "name: local\nservices:\n  - name: api\n    port: 8080\n")
	index.Observe(own)
	result = Resolve([]state.Port{docker}, nil, index)[0]
	if deref(result.Group) != "local" || result.GroupSource == nil || *result.GroupSource != state.SourceFile {
		t.Fatalf("own manifest lost precedence: %+v", result)
	}
	// A managed run remains above both manifests and Compose.
	result = Resolve([]state.Port{docker}, fakeRuns{8080: {group: "explicit", name: "api"}}, index)[0]
	if deref(result.Group) != "explicit" || *result.GroupSource != state.SourceStart {
		t.Fatalf("run lost precedence: %+v", result)
	}
}

func TestComposeLinkedCheckoutDoesNotJoinMainManifest(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.repo, ConfigName), "name: project\nservices:\n  - name: api\n    port: 8080\n")
	index := NewIndex()
	index.Observe(f.repo)
	index.AddComposeProject("linked", f.worktree)
	p := composePort(8080, "linked", "api")
	got := Resolve([]state.Port{p, p}, nil, index)
	for _, row := range got {
		if deref(row.Group) != "project@feature-x" || deref(row.ProjectRoot) != f.worktree {
			t.Fatalf("linked ownership=%+v", row)
		}
	}
	index.SetAliases(map[string]string{f.repo: "renamed"})
	got = Resolve(got, nil, index)
	for _, row := range got {
		if deref(row.Group) != "renamed@feature-x" {
			t.Fatalf("alias not reapplied=%+v", row)
		}
	}
}
