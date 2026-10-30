package groups

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func intPtr(v int) *int { return &v }

func TestAddressBookResolvesDeclarationsAndRuntime(t *testing.T) {
	cfg, err := loadString(t, "shop", `
name: shop
services:
  - name: api
    cmd: run-api
    port: 3000
  - name: worker
    cmd: run-worker
    port: auto
  - name: idle
    cmd: run-idle
    port: auto
  - name: sink
    cmd: run-sink
machine:
  - name: mysql
    port: 3306
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	group := state.Group{
		Name:       "shop",
		ConfigPath: &cfg.Path,
		RootDir:    &cfg.Dir,
		Services: []state.Service{
			{Name: "worker", Running: true, PortActual: intPtr(4100)},
			{Name: "idle", Running: false},
		},
	}

	book := cfg.AddressBook([]state.Group{group})
	if book.Worktree == "" {
		t.Error("Worktree = empty, want this checkout's token")
	}
	for name, want := range map[string]int{"api": 3000, "worker": 4100, "mysql": 3306} {
		if got := book.Ports[name]; got != want {
			t.Errorf("Ports[%s] = %d, want %d", name, got, want)
		}
	}
	if _, ok := book.Ports["idle"]; ok {
		t.Error("a stopped port: auto service has no address")
	}
	if reason := book.Unresolved["idle"]; !strings.Contains(reason, "port: auto") {
		t.Errorf("Unresolved[idle] = %q, want the port: auto reason", reason)
	}
	if _, ok := book.Ports["sink"]; ok {
		t.Error("a service that declares no port is not an address")
	}
}

func TestWorkspaceServiceAddressReadsTheOtherCheckout(t *testing.T) {
	root := tempTree(t)
	backendDir := mkdir(t, root, "backend")
	backendPath := filepath.Join(backendDir, ConfigName)
	writeFile(t, backendPath, `name: backend
services:
  - name: api
    cmd: run-api
    port: 8080
`)
	frontDir := mkdir(t, root, "frontend")
	frontPath := filepath.Join(frontDir, ConfigName)
	writeFile(t, frontPath, `name: frontend
services:
  - name: web
    cmd: run-web
workspace:
  - name: backend
    worktree: ../backend
    service: api
`)
	front, err := Load(frontPath)
	if err != nil {
		t.Fatalf("Load frontend: %v", err)
	}

	// Down, with a fixed port: the declared address is still the address.
	if port, err := front.WorkspaceServiceAddress("backend", nil); err != nil || port != 8080 {
		t.Fatalf("WorkspaceServiceAddress stopped = (%d, %v), want (8080, nil)", port, err)
	}

	// Running: runtime facts win over the declaration.
	backend, err := Load(backendPath)
	if err != nil {
		t.Fatalf("Load backend: %v", err)
	}
	groups := []state.Group{{
		Name:       "backend",
		ConfigPath: &backend.Path,
		RootDir:    &backend.Dir,
		Services:   []state.Service{{Name: "api", Running: true, PortActual: intPtr(9090)}},
	}}
	if port, err := front.WorkspaceServiceAddress("backend", groups); err != nil || port != 9090 {
		t.Fatalf("WorkspaceServiceAddress running = (%d, %v), want (9090, nil)", port, err)
	}

	book := front.AddressBook(groups)
	if book.Ports["backend"] != 9090 {
		t.Errorf("AddressBook Ports[backend] = %d, want 9090", book.Ports["backend"])
	}
	if got := Expand("${backend.url}", Values{Ports: book.Ports}); got != "http://localhost:9090" {
		t.Errorf("Expand backend.url = %q", got)
	}
}

func TestWorkspaceAutoServiceNeedsRuntimeFacts(t *testing.T) {
	root := tempTree(t)
	backendDir := mkdir(t, root, "backend")
	writeFile(t, filepath.Join(backendDir, ConfigName), `name: backend
services:
  - name: api
    cmd: run-api
    port: auto
`)
	frontDir := mkdir(t, root, "frontend")
	frontPath := filepath.Join(frontDir, ConfigName)
	writeFile(t, frontPath, `name: frontend
workspace:
  - name: backend
    worktree: `+backendDir+`
    service: api
`)
	front, err := Load(frontPath)
	if err != nil {
		t.Fatalf("Load frontend: %v", err)
	}
	if _, err := front.WorkspaceServiceAddress("backend", nil); err == nil ||
		!strings.Contains(err.Error(), "port: auto") {
		t.Fatalf("WorkspaceServiceAddress = %v, want the port: auto reason", err)
	}
}

func TestWorkspaceMissingCheckoutIsReported(t *testing.T) {
	cfg, err := loadString(t, "front", `name: front
workspace:
  - name: backend
    worktree: ../missing
    service: api
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = cfg.WorkspaceServiceAddress("backend", nil)
	if err == nil || !strings.Contains(err.Error(), "workspace backend") {
		t.Fatalf("WorkspaceServiceAddress = %v, want a named workspace error", err)
	}
}

func TestWorkspaceConfigAcceptsAManifestPath(t *testing.T) {
	root := tempTree(t)
	backendDir := mkdir(t, root, "backend")
	backendPath := filepath.Join(backendDir, ConfigName)
	writeFile(t, backendPath, "name: backend\nservices:\n  - name: api\n    port: 8080\n")
	cfg, err := loadString(t, "front", `name: front
workspace:
  - name: backend
    worktree: `+backendPath+`
    service: api
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if port, err := cfg.WorkspaceServiceAddress("backend", nil); err != nil || port != 8080 {
		t.Fatalf("WorkspaceServiceAddress = (%d, %v), want (8080, nil)", port, err)
	}
}
