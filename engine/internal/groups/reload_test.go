package groups

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	target := filepath.Join(dir, ConfigName)
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return target
}

// A project configured after the daemon started must become a group on the
// next reload; a long-lived index cannot discover it by itself.
func TestReloadFindsANewFile(t *testing.T) {
	dir := t.TempDir()
	index := NewIndex()

	if loaded, invalid := index.Reload([]string{dir}); loaded != 0 || len(invalid) != 0 {
		t.Fatalf("empty dir loaded %d configs, %v", loaded, invalid)
	}

	writeConfig(t, dir, "name: later\nservices:\n  - name: api\n    port: 8000\n")

	loaded, invalid := index.Reload([]string{dir})
	if loaded != 1 || len(invalid) != 0 {
		t.Fatalf("reload loaded %d configs, errors %v", loaded, invalid)
	}
	cfg, found := index.Named("later")
	if !found || cfg.Services[0].Name != "api" {
		t.Fatalf("the new config is missing: %+v", cfg)
	}
}

func TestReloadFollowsAnEditAndADelete(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "name: demo\nservices:\n  - name: api\n")
	index := NewIndex()
	index.Observe(dir)

	if _, found := index.Named("demo"); !found {
		t.Fatal("Observe did not index the config")
	}
	if index.Stale() {
		t.Fatal("a freshly read index reports itself stale")
	}

	writeConfig(t, dir, "name: renamed\nservices:\n  - name: api\n  - name: web\n")
	if !index.Stale() {
		t.Fatal("an edited file did not read as stale")
	}
	if loaded, invalid := index.Reload(nil); loaded != 1 || len(invalid) != 0 {
		t.Fatalf("reload after an edit: %d, %v", loaded, invalid)
	}
	cfg, found := index.Named("renamed")
	if !found || len(cfg.Services) != 2 {
		t.Fatalf("the edit was not picked up: %+v", cfg)
	}
	if _, gone := index.Named("demo"); gone {
		t.Error("the old group name survived")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !index.Stale() {
		t.Fatal("a deleted file did not read as stale")
	}
	if loaded, _ := index.Reload(nil); loaded != 0 {
		t.Fatalf("a deleted config is still loaded: %d", loaded)
	}
	if len(index.Configs()) != 0 {
		t.Errorf("Configs still returns %d entries", len(index.Configs()))
	}
}

// A broken file is a reportable error, never a fatal one, and fixing it is
// picked up by the next reload.
func TestReloadReportsInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "name: bad name\nservices:\n  - name: api\n")

	index := NewIndex()
	loaded, invalid := index.Reload([]string{dir})
	if loaded != 0 || len(invalid) != 1 {
		t.Fatalf("reload = %d, %v", loaded, invalid)
	}
	if invalid[0].Path != Canonical(filepath.Join(dir, ConfigName)) {
		t.Errorf("error names %s", invalid[0].Path)
	}

	// Two stats can land in one filesystem timestamp, so Stale also compares
	// sizes.
	time.Sleep(10 * time.Millisecond)
	writeConfig(t, dir, "name: fixed\nservices:\n  - name: api\n")
	if !index.Stale() {
		t.Fatal("fixing an invalid file did not read as stale")
	}
	if loaded, invalid := index.Reload(nil); loaded != 1 || len(invalid) != 0 {
		t.Fatalf("reload after the fix: %d, %v", loaded, invalid)
	}
}

func TestKnownAndByPath(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "name: demo\nservices:\n  - name: api\n")
	index := NewIndex()
	index.Observe(dir)

	// Observe resolves symlinks, so on macOS the indexed path is the
	// /private/var spelling — the path the daemon serves.
	known := index.Known()
	if len(known) != 1 {
		t.Fatalf("Known() = %v", known)
	}
	if filepath.Base(known[0]) != ConfigName {
		t.Fatalf("Known() = %v", known)
	}
	if cfg, found := index.ByPath(known[0]); !found || cfg.Name != "demo" {
		t.Fatalf("ByPath(%s) = %+v, %v", known[0], cfg, found)
	}
	// The path the user typed resolves too, symlinked temp dir and all.
	if cfg, found := index.ByPath(path); !found || cfg.Name != "demo" {
		t.Fatalf("ByPath(%s) = %+v, %v", path, cfg, found)
	}
	if _, found := index.ByPath(filepath.Join(dir, "nope.yaml")); found {
		t.Error("ByPath matched a file that is not indexed")
	}
}
