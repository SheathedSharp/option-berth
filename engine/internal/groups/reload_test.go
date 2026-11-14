package groups

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	configPath := filepath.Join(dir, ConfigName)
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", configPath, err)
	}
	return configPath
}

// manifestIndexState is the caller-visible result of a reload, not an
// implementation snapshot: usable groups, errors, and the canonical file keys.
type manifestIndexState struct {
	cfg     *Config
	invalid *ConfigError
}

func requireConfig(t *testing.T, got *Config, want *Config) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("config = %#v, want %#v", got, want)
	}
}

func assertManifestIndex(t *testing.T, x *Index, path string, roots []string, want manifestIndexState) {
	t.Helper()
	loaded, bad := x.Reload(roots)
	canon := Canonical(path)
	if want.invalid != nil {
		ce := *want.invalid
		if ce.Path == "" {
			ce.Path = canon
		}
		if loaded != 0 || x.Configs() != nil {
			t.Fatalf("loaded/configs = %d/%v, want 0/nil", loaded, x.Configs())
		}
		if len(bad) != 1 {
			t.Fatalf("invalid files = %#v, want 1", bad)
		}
		if bad[0].Path != canon {
			t.Errorf("error path = %s, want %s", bad[0].Path, canon)
		}
		var got *ConfigError
		if !errors.As(bad[0].Err, &got) || !reflect.DeepEqual(got, &ce) {
			t.Errorf("invalid error = %#v (%v), want %#v", bad[0].Err, bad[0].Err, &ce)
		}
		if !reflect.DeepEqual(x.Known(), []string{canon}) {
			t.Errorf("Known() = %v, want the invalid path still listed", x.Known())
		}
		if _, ok := x.ByPath(path); ok {
			t.Error("ByPath returned a config for an invalid file")
		}
		if x.Stale() {
			t.Fatal("reloaded index is stale")
		}
		return
	}
	if !reflect.DeepEqual(bad, []InvalidConfig{}) {
		t.Fatalf("invalid files = %#v, want none", bad)
	}
	if want.cfg == nil {
		if loaded != 0 || x.Configs() != nil {
			t.Fatalf("loaded/configs = %d/%v, want 0/nil", loaded, x.Configs())
		}
		if !reflect.DeepEqual(x.Known(), []string{}) {
			t.Errorf("Known() = %v, want an empty list", x.Known())
		}
		if _, ok := x.ByPath(path); ok {
			t.Error("ByPath returned a config when nothing is loaded")
		}
		if x.Stale() {
			t.Fatal("reloaded index is stale")
		}
		return
	}
	cfg := *want.cfg
	if cfg.Path == "" {
		cfg.Path = canon
	}
	if cfg.Dir == "" {
		cfg.Dir = filepath.Dir(canon)
	}
	if loaded != 1 || len(x.Configs()) != 1 {
		t.Fatalf("loaded/configs = %d/%d, want 1", loaded, len(x.Configs()))
	}
	requireConfig(t, x.Configs()[0], &cfg)
	got, found := x.Named(cfg.Name)
	if !found {
		t.Fatalf("Named(%q) missed reloaded manifest", cfg.Name)
	}
	requireConfig(t, got, &cfg)
	if byPath, ok := x.ByPath(path); !ok {
		t.Fatalf("ByPath(%s) missed reloaded manifest", path)
	} else {
		requireConfig(t, byPath, &cfg)
	}
	if !reflect.DeepEqual(x.Known(), []string{canon}) {
		t.Errorf("Known() = %v, want [%s]", x.Known(), canon)
	}
	if x.Stale() {
		t.Fatal("reloaded index is stale")
	}
}

// The lifecycle keeps root discovery separate from subsequent reloads: once
// observed, edits, invalid files and deletion must be reconciled with nil roots.
func TestReloadPicksUpANewFile(t *testing.T) {
	dir := t.TempDir()
	x := NewIndex()
	assertManifestIndex(t, x, filepath.Join(dir, ConfigName), []string{dir}, manifestIndexState{})
	path := writeConfig(t, dir, "name: later\nservices:\n  - name: api\n    port: 8000\n")
	assertManifestIndex(t, x, path, []string{dir}, manifestIndexState{
		cfg: &Config{Name: "later", Services: []Service{{Name: "api", Port: 8000}}},
	})
}

func TestReloadPicksUpAnEditAndADelete(t *testing.T) {
	root := t.TempDir()
	configPath := writeConfig(t, root, "name: demo\nservices:\n  - name: api\n")
	x := NewIndex()
	x.Observe(root)
	if _, found := x.Named("demo"); !found {
		t.Fatal("Observe did not index the config")
	}
	if x.Stale() {
		t.Fatal("freshly observed manifest is stale")
	}
	requireConfig(t, x.Configs()[0], &Config{
		Name: "demo", Dir: Canonical(root), Path: Canonical(configPath),
		Services: []Service{{Name: "api"}},
	})
	for _, edit := range []struct {
		title, body string
		remove      bool
		state       manifestIndexState
	}{
		{title: "edit", body: "name: renamed\nservices:\n  - name: api\n  - name: web\n", state: manifestIndexState{cfg: &Config{Name: "renamed", Services: []Service{{Name: "api"}, {Name: "web"}}}}},
		{title: "delete", remove: true},
	} {
		t.Run(edit.title, func(t *testing.T) {
			if edit.remove {
				if err := os.Remove(configPath); err != nil {
					t.Fatal(err)
				}
			} else {
				writeConfig(t, root, edit.body)
			}
			if !x.Stale() {
				t.Fatal("changed manifest did not invalidate the index")
			}
			assertManifestIndex(t, x, configPath, nil, edit.state)
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
	assertManifestIndex(t, x, path, []string{dir}, manifestIndexState{
		invalid: &ConfigError{Problems: []string{`name "bad name" contains " "; names may not contain slashes or whitespace`}},
	})
	// The byte change, not a sleep or a timestamp tick, is the freshness proof.
	writeConfig(t, dir, "name: fixed\nservices:\n  - name: api\n")
	if !x.Stale() {
		t.Fatal("repairing an invalid file did not invalidate its observation")
	}
	assertManifestIndex(t, x, path, nil, manifestIndexState{
		cfg: &Config{Name: "fixed", Services: []Service{{Name: "api"}}},
	})
}

// TestKnownAndByPath are what the runtime service's config handlers resolve through.
func TestKnownAndByPath(t *testing.T) {
	root := t.TempDir()
	configPath := writeConfig(t, root, "name: demo\nservices:\n  - name: api\n")
	x := NewIndex()
	x.Observe(root)

	// Observe resolves symlinks, so on macOS the indexed path is the
	// /private/var spelling of the temp dir — the path the runtime service serves.
	want := &Config{
		Name: "demo", Dir: Canonical(root), Path: Canonical(configPath),
		Services: []Service{{Name: "api"}},
	}
	if !reflect.DeepEqual(x.Known(), []string{Canonical(configPath)}) {
		t.Fatalf("Known() = %v, want [%s]", x.Known(), Canonical(configPath))
	}
	if cfg, ok := x.ByPath(x.Known()[0]); !ok {
		t.Fatalf("ByPath(%s) missed the indexed path", x.Known()[0])
	} else {
		requireConfig(t, cfg, want)
	}
	// The path the user typed resolves too, symlinked temp dir and all.
	if cfg, ok := x.ByPath(configPath); !ok {
		t.Fatalf("ByPath(%s) missed the typed path", configPath)
	} else {
		requireConfig(t, cfg, want)
	}
	if _, ok := x.ByPath(filepath.Join(root, "nope.yaml")); ok {
		t.Error("ByPath matched a file that is not indexed")
	}
}
