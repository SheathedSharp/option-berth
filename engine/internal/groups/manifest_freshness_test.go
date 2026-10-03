package groups

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

func replaceManifestSameStamp(t *testing.T, path, body string) {
	t.Helper()
	old, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(body)) != old.Size() {
		t.Fatal("fixture must preserve length")
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, old.ModTime(), old.ModTime()); err != nil {
		t.Fatal(err)
	}
	now, err := os.Stat(path)
	if err != nil || now.Size() != old.Size() || !now.ModTime().Equal(old.ModTime()) {
		t.Fatal("fixture did not preserve file metadata")
	}
}

func TestManifestFreshnessSameStamp(t *testing.T) {
	dir := Canonical(t.TempDir())
	path := writeConfig(t, dir, "name: before\nports: [8080]\n")
	x := NewIndex()
	x.Observe(dir)
	held := x.At(dir)
	replaceManifestSameStamp(t, path, "name: afterx\nports: [9090]\n")
	if !x.Stale() {
		t.Fatal("same-stamp content replacement not detected")
	}
	// Re-attribution, not only an explicit reload, must see the new declaration.
	rows, _ := AttributeWith([]ports.ListeningPort{{Port: 9090, Cwd: dir}}, NoRuns{}, x)
	if x.At(dir) == nil || x.At(dir).Name != "afterx" || deref(rows[0].Group) != "afterx" {
		t.Fatal("attribution used superseded manifest bytes")
	}
	if _, _, ok := x.MatchPort(state.Port{Port: 8080}); ok {
		t.Fatal("superseded claim survived")
	}
	if held.Name != "before" || held.Ports[0] != 8080 {
		t.Fatal("published config was mutated")
	}
	if x.Stale() {
		t.Fatal("newly observed content still stale")
	}
}

func TestManifestFreshnessInvalidRevokesClaim(t *testing.T) {
	dir := Canonical(t.TempDir())
	path := writeConfig(t, dir, "name: before\nports: [8080]\n")
	x := NewIndex()
	x.Observe(dir)
	_, _, _ = x.MatchPort(state.Port{Port: 8080}) // build the derived index first
	if err := os.WriteFile(path, []byte("name: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := x.LoadFile(path); err == nil {
		t.Fatal("invalid fixture accepted")
	}
	if x.At(dir) != nil {
		t.Fatal("invalid replacement retained old valid config")
	}
	if _, _, ok := x.MatchPort(state.Port{Port: 8080}); ok {
		t.Fatal("invalid replacement retained port claim")
	}
	if len(x.Invalid()) != 1 {
		t.Fatal("invalid replacement not reported")
	}
	if err := os.WriteFile(path, []byte("name: fixed\nports: [9090]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	x.Observe(dir)
	if x.At(dir) == nil || x.At(dir).Name != "fixed" || len(x.Invalid()) != 0 {
		t.Fatal("repaired declaration did not recover")
	}
}

func TestManifestFreshnessPriorityWithSameDirectoryStamp(t *testing.T) {
	dir := Canonical(t.TempDir())
	legacy := filepath.Join(dir, LegacyConfigName)
	writeFile(t, legacy, "name: legacy\nports: [8080]\n")
	staged := filepath.Join(dir, "staged.yaml")
	writeFile(t, staged, "name: modern\nports: [9090]\n")
	x := NewIndex()
	x.Observe(dir)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, filepath.Join(dir, ConfigName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if !x.Stale() {
		t.Fatal("new preferred file hidden behind unchanged legacy file")
	}
	x.Reload(nil)
	if cfg := x.At(dir); cfg == nil || cfg.Name != "modern" {
		t.Fatal("preferred file not selected")
	}
	if err := os.Remove(filepath.Join(dir, ConfigName)); err != nil {
		t.Fatal(err)
	}
	x.Observe(dir)
	if cfg := x.At(dir); cfg == nil || cfg.Name != "legacy" {
		t.Fatal("fallback file not selected after removal")
	}
	if len(x.Known()) != 1 {
		t.Fatal("retired file still retained as active")
	}
}

func TestManifestFreshnessObserveDeletion(t *testing.T) {
	dir := Canonical(t.TempDir())
	path := writeConfig(t, dir, "name: demo\nports: [8080]\n")
	x := NewIndex()
	x.Observe(dir)
	_, _, _ = x.MatchPort(state.Port{Port: 8080})
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	x.Observe(dir)
	if len(x.Configs()) != 0 || len(x.Known()) != 0 || len(x.Invalid()) != 0 {
		t.Fatal("deleted manifest retained a declaration")
	}
	if _, _, ok := x.MatchPort(state.Port{Port: 8080}); ok {
		t.Fatal("deleted claim survived")
	}
	writeConfig(t, dir, "name: reborn\nports: [9090]\n")
	x.Reload(nil)
	if cfg := x.At(dir); cfg == nil || cfg.Name != "reborn" {
		t.Fatal("recreated manifest in known directory was lost")
	}
}

func TestManifestFreshnessStableReloadRetainsParsedObjects(t *testing.T) {
	dir := Canonical(t.TempDir())
	path := writeConfig(t, dir, "name: demo\nports: [8080]\n")
	x := NewIndex()
	x.Observe(dir)
	held := x.At(dir)
	_, _, _ = x.MatchPort(state.Port{Port: 8080})
	version := x.claimVersion
	for i := 0; i < 3; i++ {
		x.Reload(nil)
		if x.At(dir) != held {
			t.Fatal("unchanged reload reparsed config")
		}
		_, _, _ = x.MatchPort(state.Port{Port: 8080})
		if x.claimVersion != version {
			t.Fatal("unchanged reload rebuilt claim index")
		}
	}
	replaceManifestSameStamp(t, path, "name: demo\nports: [9090]\n")
	x.Reload(nil)
	if x.At(dir) == held {
		t.Fatal("changed content incorrectly reused parsed config")
	}
}

func TestManifestFreshnessRefreshOnlyChangedDirectory(t *testing.T) {
	a, b := Canonical(t.TempDir()), Canonical(t.TempDir())
	ap := writeConfig(t, a, "name: first\nports: [8080]\n")
	writeConfig(t, b, "name: other\nports: [9090]\n")
	x := NewIndex()
	x.Reload([]string{a, b})
	held := x.At(b)
	replaceManifestSameStamp(t, ap, "name: newer\nports: [8080]\n")
	x.Reload(nil)
	if x.At(a).Name != "newer" {
		t.Fatal("changed directory missed")
	}
	if x.At(b) != held {
		t.Fatal("one edit reparsed an unrelated directory")
	}
}

func TestManifestFreshnessBrokenPreferredFileBlocksFallback(t *testing.T) {
	dir := Canonical(t.TempDir())
	legacy := filepath.Join(dir, LegacyConfigName)
	writeFile(t, legacy, "name: legacy\nports: [8080]\n")
	x := NewIndex()
	x.Observe(dir)
	preferred := filepath.Join(dir, ConfigName)
	writeFile(t, preferred, "name: [\n")
	x.Observe(dir)
	if len(x.Configs()) != 0 || len(x.Invalid()) != 1 {
		t.Fatal("invalid preferred declaration silently used fallback")
	}
	if err := os.Remove(preferred); err != nil {
		t.Fatal(err)
	}
	x.Observe(dir)
	if cfg := x.At(dir); cfg == nil || cfg.Name != "legacy" || len(x.Invalid()) != 0 {
		t.Fatal("fallback did not recover after invalid preferred file was removed")
	}
}

func TestManifestFreshnessAtomicReplacement(t *testing.T) {
	dir := Canonical(t.TempDir())
	path := writeConfig(t, dir, "name: before\nports: [8080]\n")
	x := NewIndex()
	x.Observe(dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "replacement")
	writeFile(t, replacement, "name: afterx\nports: [9090]\n")
	if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	x.Reload(nil)
	if x.At(dir) == nil || x.At(dir).Name != "afterx" {
		t.Fatal("atomic replacement not observed")
	}
}

func TestManifestFreshnessKnownEmptyRoot(t *testing.T) {
	dir := Canonical(t.TempDir())
	x := NewIndex()
	x.Reload([]string{dir})
	writeConfig(t, dir, "name: appeared\n")
	x.Reload(nil)
	if cfg := x.At(dir); cfg == nil || cfg.Name != "appeared" {
		t.Fatal("explicit empty root was forgotten")
	}
}

func TestManifestFreshnessSymlinkRetarget(t *testing.T) {
	origin, a, b := Canonical(t.TempDir()), Canonical(t.TempDir()), Canonical(t.TempDir())
	ap := writeConfig(t, a, "name: first\nports: [8080]\n")
	bp := writeConfig(t, b, "name: other\nports: [9090]\n")
	link := filepath.Join(origin, ConfigName)
	if err := os.Symlink(ap, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	x := NewIndex()
	x.Observe(origin)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bp, link); err != nil {
		t.Fatal(err)
	}
	x.Reload(nil)
	if x.At(a) != nil || x.At(b) == nil || x.At(b).Name != "other" {
		t.Fatal("retarget retained old canonical declaration")
	}
}

func TestManifestFreshnessSharedSymlinkTarget(t *testing.T) {
	target, a, b := Canonical(t.TempDir()), Canonical(t.TempDir()), Canonical(t.TempDir())
	path := writeConfig(t, target, "name: target\nports: [8080]\n")
	for _, dir := range []string{a, b} {
		if err := os.Symlink(path, filepath.Join(dir, ConfigName)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	x := NewIndex()
	x.Observe(a)
	x.Observe(b)
	if err := os.Remove(filepath.Join(b, ConfigName)); err != nil {
		t.Fatal(err)
	}
	x.Observe(b)
	if x.At(target) == nil {
		t.Fatal("removing one alias erased the other contribution")
	}
	writeFile(t, path, "name: [\n")
	if err := x.LoadFile(filepath.Join(a, ConfigName)); err == nil {
		t.Fatal("invalid fixture accepted")
	}
	if x.At(target) != nil {
		t.Fatal("failed alias reload left canonical claim active")
	}
}
