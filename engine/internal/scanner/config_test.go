package scanner

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// TestScanPicksUpAnEditedConfig: the index a daemon keeps is long-lived, so a
// file edited underneath it would otherwise stay as it was first read. The
// stat it costs per known file on each tick is what keeps it honest — no
// watcher, no new dependency.
func TestScanPicksUpAnEditedConfig(t *testing.T) {
	root := resolved(t, t.TempDir())
	writeConfig(t, root, `
name: before
services:
  - name: api
    port: 8000
`)

	loop := loopWith(nil, ports.ListeningPort{
		Port: 8000, Process: "python3", PID: 42, Cwd: root,
	})
	first, err := loop.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(first.Groups) != 1 || first.Groups[0].Name != "before" {
		t.Fatalf("groups = %+v", first.Groups)
	}

	time.Sleep(10 * time.Millisecond)
	writeConfig(t, root, `
name: after
services:
  - name: api
    port: 8000
    description: edited
`)
	loop.Invalidate()

	second, err := loop.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot after the edit: %v", err)
	}
	if len(second.Groups) != 1 || second.Groups[0].Name != "after" {
		t.Fatalf("groups after the edit = %+v", second.Groups)
	}
	svc := second.Groups[0].Services[0]
	if svc.Description == nil || *svc.Description != "edited" {
		t.Fatalf("the edited metadata did not reach the snapshot: %+v", svc)
	}
}

// TestReloadConfigsPicksUpANewFile is `groups.reload` at the scanner level: a
// root the store remembers gets re-read even though no process has been seen in
// it yet.
func TestReloadConfigsPicksUpANewFile(t *testing.T) {
	st := openStore(t)
	root := resolved(t, t.TempDir())
	if err := st.AddRoot(root); err != nil {
		t.Fatal(err)
	}

	loop := loopWith(st)
	if _, err := loop.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if n := len(loop.Configs()); n != 0 {
		t.Fatalf("configs = %d, want none before the file exists", n)
	}

	path := filepath.Join(root, ".oberth.yaml")
	if err := os.WriteFile(path, []byte(`
name: fresh
services:
  - name: api
`), 0o600); err != nil {
		t.Fatal(err)
	}

	valid, invalid := loop.ReloadConfigs()
	if valid != 1 || len(invalid) != 0 {
		t.Fatalf("ReloadConfigs = %d, %v", valid, invalid)
	}
	if cfg, ok := loop.ConfigNamed("fresh"); !ok || cfg.Services[0].Name != "api" {
		t.Fatalf("ConfigNamed(fresh) = %+v, %v", cfg, ok)
	}
	if _, ok := loop.ConfigAt(path); !ok {
		t.Error("ConfigAt does not find the file that was just loaded")
	}
}
