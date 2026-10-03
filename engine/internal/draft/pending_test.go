package draft

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/paths"
)

// TestPendingForReadsWhatArchiveWrote: the header is written by this package and
// read by this package, so the round trip is what has to hold — the aggregate
// reports the provenance, and a parse that drifted would report empty fields
// without failing anything.
func TestPendingForReadsWhatArchiveWrote(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)

	path := paths.Draft("berth-demo")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "# Drafted by codex (codex-cli 0.154.0, pinned 2026-09-23) on 2026-09-23 16:58, from /Users/you/code/berth-demo.\n" +
		"# A draft, not a declaration: nothing here is a service until you adopt it —\n" +
		"\nname: berth-demo\nservices: []\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := PendingFor("berth-demo")
	if !ok {
		t.Fatal("the draft that was just written is not pending")
	}
	if got.Path != path || got.Group != "berth-demo" {
		t.Errorf("pending = %+v, want it keyed by the group", got)
	}
	if got.Agent != "codex" {
		t.Errorf("agent = %q, want codex", got.Agent)
	}
	if got.At != "2026-09-23 16:58" {
		t.Errorf("at = %q, want the stamp archive wrote", got.At)
	}
	if got.Root != "/Users/you/code/berth-demo" {
		t.Errorf("root = %q, want the directory it was drafted from", got.Root)
	}
}

// TestPendingForIsQuietAboutWhatItCannotRead: a draft whose header is missing
// is still a draft — reporting it with empty provenance beats hiding it.
func TestPendingForIsQuietAboutWhatItCannotRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)

	path := paths.Draft("odd")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("name: odd\nservices: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := PendingFor("odd")
	if !ok {
		t.Fatal("a draft with no header is not pending")
	}
	if got.Agent != "" || got.At != "" || got.Root != "" {
		t.Errorf("provenance = %+v, want it empty rather than invented", got)
	}

	if _, ok := PendingFor("nothing-here"); ok {
		t.Error("a group with no draft has one")
	}
	if _, ok := PendingFor(""); ok {
		t.Error(`PendingFor("") found a draft`)
	}
}
