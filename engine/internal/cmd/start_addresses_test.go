package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func writeManifest(t *testing.T, dir, body string) *groups.Config {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, groups.ConfigName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := groups.Load(path)
	if err != nil {
		t.Fatalf("Load %s: %v", path, err)
	}
	return cfg
}

func TestExpandStartAddressesUsesThisWorktreesFixedPorts(t *testing.T) {
	cfg := writeManifest(t, t.TempDir(), `name: shop
services:
  - name: api
    cmd: run-api
    port: 3000
machine:
  - name: mysql
    port: 3306
`)
	got, err := expandStartAddresses(context.Background(), cfg,
		[]string{"curl", "${api.url}/health", "${mysql.port}", "${HOME}"}, 0)
	if err != nil {
		t.Fatalf("expandStartAddresses: %v", err)
	}
	want := []string{"curl", "http://localhost:3000/health", "3306", "${HOME}"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("expanded = %q, want %q", got, want)
	}
}

func TestExpandStartAddressesExpandsWorktreeToken(t *testing.T) {
	dir := t.TempDir()
	cfg := writeManifest(t, dir, "name: shop\nservices:\n  - name: api\n    cmd: run-api\n    port: 3000\n")
	got, err := expandStartAddresses(context.Background(), cfg, []string{"--data", "${worktree}"}, 0)
	if err != nil {
		t.Fatalf("expandStartAddresses: %v", err)
	}
	token := groups.WorktreeTokenFor(cfg.Dir)
	if got[1] != token || token == "" {
		t.Fatalf("worktree expansion = %q, want %q", got[1], token)
	}
}

func TestExpandStartAddressesBarePortNeedsTheFlag(t *testing.T) {
	cfg := writeManifest(t, t.TempDir(), "name: shop\nservices:\n  - name: api\n    cmd: run-api\n    port: 3000\n")
	got, err := expandStartAddresses(context.Background(), cfg, []string{"--port", "${port}"}, 4242)
	if err != nil {
		t.Fatalf("expandStartAddresses: %v", err)
	}
	if got[1] != "4242" {
		t.Fatalf("bare ${port} with --port = %q, want 4242", got[1])
	}
	left, err := expandStartAddresses(context.Background(), cfg, []string{"--port", "${port}"}, 0)
	if err != nil {
		t.Fatalf("expandStartAddresses without --port: %v", err)
	}
	if left[1] != "${port}" {
		t.Fatalf("bare ${port} without --port = %q, want it left alone", left[1])
	}
}

func TestExpandStartAddressesRefusesUnknownReferences(t *testing.T) {
	cfg := writeManifest(t, t.TempDir(), "name: shop\nservices:\n  - name: api\n    cmd: run-api\n    port: 3000\n")
	_, err := expandStartAddresses(context.Background(), cfg, []string{"curl", "${ghost.url}"}, 0)
	if err == nil || !strings.Contains(err.Error(), "ghost names no service") {
		t.Fatalf("expandStartAddresses error = %v, want it to name ghost", err)
	}
}

func TestExpandStartAddressesResolvesAnotherCheckout(t *testing.T) {
	root := t.TempDir()
	backend := writeManifest(t, filepath.Join(root, "backend"), "name: backend\nservices:\n  - name: api\n    cmd: run-api\n    port: 8080\n")
	frontDir := filepath.Join(root, "frontend")
	front := writeManifest(t, frontDir, `name: frontend
workspace:
  - name: backend
    worktree: ../backend
    service: api
`)
	// The frontend manifest is real; the backend path must resolve through it.
	if backend.Dir == front.Dir {
		t.Fatal("test checkouts must differ")
	}
	got, err := expandStartAddresses(context.Background(), front, []string{"curl", "${backend.url}"}, 0)
	if err != nil {
		t.Fatalf("expandStartAddresses: %v", err)
	}
	if got[1] != "http://localhost:8080" {
		t.Fatalf("workspace address = %q, want http://localhost:8080", got[1])
	}
}

func TestExpandStartAddressesWithoutAManifestLeavesTheCommandAlone(t *testing.T) {
	got, err := expandStartAddresses(context.Background(), nil, []string{"echo", "${api.url}"}, 0)
	if err != nil {
		t.Fatalf("expandStartAddresses: %v", err)
	}
	if got[1] != "${api.url}" {
		t.Fatalf("command changed without a manifest: %q", got)
	}
}

// TestOverlayRunAddressesFillsAssignedPorts: `up` reports the assigned port
// before the scanner publishes the listener. The run's PortHint closes that
// gap, and the manifest path keeps another checkout's run from filling this
// one's row.
func TestOverlayRunAddressesFillsAssignedPorts(t *testing.T) {
	root := t.TempDir()
	localPath := filepath.Join(root, "front", groups.ConfigName)
	remotePath := filepath.Join(root, "backend", groups.ConfigName)
	localPort, remotePort := 4100, 4200
	groupsIn := []state.Group{{
		ConfigPath: &localPath,
		Services:   []state.Service{{Name: "api", PortAuto: true}},
	}}

	out := overlayRunAddresses(groupsIn, []rpc.RunRecord{
		{ConfigPath: localPath, Name: "api", PortHint: &localPort, Status: "starting"},
		{ConfigPath: remotePath, Name: "api", PortHint: &remotePort, Status: "running"},
		{ConfigPath: "", Name: "api", PortHint: &localPort, Status: "running"},
	})

	var local *state.Group
	for i := range out {
		if out[i].ConfigPath != nil && *out[i].ConfigPath == localPath {
			local = &out[i]
		}
	}
	if local == nil || len(local.Services) != 1 {
		t.Fatalf("local group = %+v", local)
	}
	if local.Services[0].PortActual == nil || *local.Services[0].PortActual != localPort || !local.Services[0].Running {
		t.Fatalf("local service = %+v, want the assigned port from the run", local.Services[0])
	}
	sawRemote := false
	for _, g := range out {
		if g.ConfigPath == nil || *g.ConfigPath != remotePath {
			continue
		}
		sawRemote = true
		if len(g.Services) != 1 || g.Services[0].PortActual == nil || *g.Services[0].PortActual != remotePort {
			t.Fatalf("remote group = %+v, want its own run's port", g)
		}
	}
	if !sawRemote {
		t.Fatal("a run with a manifest path did not create the remote group")
	}
}
