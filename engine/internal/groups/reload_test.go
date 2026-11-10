package groups

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, ConfigName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// manifestIndexState is the caller-visible result of a reload, not an
// implementation snapshot: usable groups, errors, and the canonical file keys.
type manifestIndexState struct {
	group    string
	services []string
	invalid  bool
}

func assertManifestIndex(t *testing.T, x *Index, path string, roots []string, want manifestIndexState) {
	t.Helper()
	loaded, bad := x.Reload(roots)
	count := 0
	if want.group != "" {
		count = 1
	}
	if loaded != count || len(x.Configs()) != count {
		t.Fatalf("loaded/configs = %d/%d, want %d", loaded, len(x.Configs()), count)
	}
	errors := 0
	if want.invalid {
		errors = 1
	}
	if len(bad) != errors {
		t.Fatalf("invalid files = %v, want %d", bad, errors)
	}
	if want.invalid && bad[0].Path != Canonical(path) {
		t.Errorf("error path = %s, want %s", bad[0].Path, Canonical(path))
	}
	if want.group == "" {
		return
	}
	cfg, found := x.Named(want.group)
	if !found {
		t.Fatalf("Named(%q) missed reloaded manifest", want.group)
	}
	if got := names(cfg); !reflect.DeepEqual(got, want.services) {
		t.Errorf("services = %v, want %v", got, want.services)
	}
}

// The lifecycle keeps root discovery separate from subsequent reloads: once
// observed, edits, invalid files and deletion must be reconciled with nil roots.
func TestReloadPicksUpANewFile(t *testing.T) {
	dir := t.TempDir()
	x := NewIndex()
	assertManifestIndex(t, x, filepath.Join(dir, ConfigName), []string{dir}, manifestIndexState{})
	path := writeConfig(t, dir, "name: later\nservices:\n  - name: api\n    port: 8000\n")
	assertManifestIndex(t, x, path, []string{dir}, manifestIndexState{group: "later", services: []string{"api"}})
}

func TestReloadPicksUpAnEditAndADelete(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "name: demo\nservices:\n  - name: api\n")
	x := NewIndex()
	x.Observe(dir)
	if _, found := x.Named("demo"); !found {
		t.Fatal("Observe did not index the config")
	}
	if x.Stale() {
		t.Fatal("freshly observed manifest is stale")
	}
	for _, edit := range []struct {
		title, body string
		remove      bool
		state       manifestIndexState
	}{
		{title: "edit", body: "name: renamed\nservices:\n  - name: api\n  - name: web\n", state: manifestIndexState{group: "renamed", services: []string{"api", "web"}}},
		{title: "delete", remove: true},
	} {
		t.Run(edit.title, func(t *testing.T) {
			if edit.remove {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				writeConfig(t, dir, edit.body)
			}
			if !x.Stale() {
				t.Fatal("changed manifest did not invalidate the index")
			}
			assertManifestIndex(t, x, path, nil, edit.state)
			if _, found := x.Named("demo"); found {
				t.Error("the pre-edit name is still published")
			}
		})
	}
}

func TestReloadReportsInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "name: bad name\nservices:\n  - name: api\n")
	x := NewIndex()
	assertManifestIndex(t, x, path, []string{dir}, manifestIndexState{invalid: true})
	// The byte change, not a sleep or a timestamp tick, is the freshness proof.
	writeConfig(t, dir, "name: fixed\nservices:\n  - name: api\n")
	if !x.Stale() {
		t.Fatal("repairing an invalid file did not invalidate its observation")
	}
	assertManifestIndex(t, x, path, nil, manifestIndexState{group: "fixed", services: []string{"api"}})
}

// TestKnownAndByPath are what the daemon's config handlers resolve through.
func TestKnownAndByPath(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "name: demo\nservices:\n  - name: api\n")
	x := NewIndex()
	x.Observe(dir)

	// Observe resolves symlinks, so on macOS the indexed path is the
	// /private/var form of the temp dir; that is the path the daemon serves.
	known := x.Known()
	if len(known) != 1 {
		t.Fatalf("Known() = %v", known)
	}
	if filepath.Base(known[0]) != ConfigName {
		t.Fatalf("Known() = %v", known)
	}
	if cfg, ok := x.ByPath(known[0]); !ok || cfg.Name != "demo" {
		t.Fatalf("ByPath(%s) = %+v, %v", known[0], cfg, ok)
	}
	// The path the user typed resolves too, symlinked temp dir and all.
	if cfg, ok := x.ByPath(path); !ok || cfg.Name != "demo" {
		t.Fatalf("ByPath(%s) = %+v, %v", path, cfg, ok)
	}
	if _, ok := x.ByPath(filepath.Join(dir, "nope.yaml")); ok {
		t.Error("ByPath matched a file that is not indexed")
	}
}
