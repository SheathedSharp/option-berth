package groups

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func loadBody(t *testing.T, dirName, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(mkdir(t, tempTree(t), dirName), ConfigName)
	writeFile(t, path, body)
	return Load(path)
}

// loadString keeps the older test files on the shared loader.
func loadString(t *testing.T, dirName, body string) (*Config, error) {
	t.Helper()
	return loadBody(t, dirName, body)
}

func TestLoadValidConfig(t *testing.T) {
	cfg, err := loadBody(t, "option-berth", `
name: option-berth
services:
  - name: db
    prepare: mvn -DskipTests package
    cmd: docker compose up db
    port: 5433
    health: /
  - name: api
    cmd: uv run uvicorn app:app
    cwd: backend
    port: 8001
    depends_on: [db]
ports: [9230]
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Name != "option-berth" {
		t.Errorf("name = %q, want option-berth", cfg.Name)
	}
	if len(cfg.Services) != 2 || cfg.Services[1].Name != "api" {
		t.Fatalf("services = %+v", cfg.Services)
	}
	if cfg.Services[0].Prepare != "mvn -DskipTests package" {
		t.Errorf("prepare = %q, want build command", cfg.Services[0].Prepare)
	}
	if got, want := cfg.ServiceDir(cfg.Services[1]), filepath.Join(cfg.Dir, "backend"); got != want {
		t.Errorf("ServiceDir = %q, want %q", got, want)
	}
	if len(cfg.Ports) != 1 || cfg.Ports[0] != 9230 {
		t.Errorf("ports = %v", cfg.Ports)
	}
}

func TestLoadDefaultsNameToDirectory(t *testing.T) {
	cfg, err := loadBody(t, "storefront", "services: []\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Name != "storefront" {
		t.Errorf("name = %q, want storefront (the directory name)", cfg.Name)
	}
}

// `machine:` is how a file says it depends on a service the machine runs — a
// database started at login, which option-berth never starts or stops (decision
// 0010). It survives a round trip: an edit through the app must not drop it.
func TestMachineReferences(t *testing.T) {
	cfg, err := loadBody(t, "shop", `
name: shop
services:
  - name: api
    cmd: uv run api
    port: 8000
machine:
  - name: mysql
    port: 3306
  - name: redis
    port: 6379
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Machine) != 2 || cfg.Machine[0].Name != "mysql" || cfg.Machine[0].Port != 3306 {
		t.Fatalf("machine = %+v", cfg.Machine)
	}
	if len(cfg.Services) != 1 {
		t.Errorf("a reference leaked into the services list: %+v", cfg.Services)
	}

	out, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := Parse(cfg.Path, out)
	if err != nil {
		t.Fatalf("the marshalled file does not load: %v\n%s", err, out)
	}
	if len(back.Machine) != 2 || back.Machine[1].Name != "redis" || back.Machine[1].Port != 6379 {
		t.Errorf("round trip lost the machine list:\n%s", out)
	}
}

func TestLoadValidationProblems(t *testing.T) {
	tests := []struct {
		label    string
		manifest string
		mentions string
	}{
		{
			label:    "name with a slash",
			manifest: "name: acme/web\n",
			mentions: `name "acme/web" contains "/"`,
		},
		{
			label:    "name with whitespace",
			manifest: "name: my project\n",
			mentions: `contains " "`,
		},
		{
			label:    "duplicate service names",
			manifest: "name: p\nservices:\n  - name: api\n  - name: api\n",
			mentions: "service api: duplicate name",
		},
		{
			label:    "cwd escapes the config directory",
			manifest: "name: p\nservices:\n  - name: api\n    cwd: ../elsewhere\n",
			mentions: "escapes the directory",
		},
		{
			label:    "absolute cwd",
			manifest: "name: p\nservices:\n  - name: api\n    cwd: /etc\n",
			mentions: "escapes the directory",
		},
		{
			label:    "port out of range",
			manifest: "name: p\nservices:\n  - name: api\n    port: 70000\n",
			mentions: "port 70000 is out of range",
		},
		{
			label:    "worktree_ports of zero",
			manifest: "name: p\nworktree_ports: 0\n",
			mentions: "worktree_ports: 0 is out of range 1-100",
		},
		{
			label:    "worktree_ports above the cap",
			manifest: "name: p\nworktree_ports: 101\n",
			mentions: "worktree_ports: 101 is out of range 1-100",
		},
		{
			label:    "worktree_ports that is not a number",
			manifest: "name: p\nworktree_ports: lots\n",
			mentions: "lots",
		},
		{
			label:    "zero port in the ports list",
			manifest: "name: p\nports: [0]\n",
			mentions: "ports: 0 is out of range",
		},
		{
			label:    "depends_on cycle",
			manifest: "name: p\nservices:\n  - name: a\n    depends_on: [b]\n  - name: b\n    depends_on: [a]\n",
			mentions: "depends_on has a cycle",
		},
		{
			label:    "depends_on self cycle",
			manifest: "name: p\nservices:\n  - name: a\n    depends_on: [a]\n",
			mentions: "depends_on has a cycle",
		},
		{
			label:    "depends_on unknown service",
			manifest: "name: p\nservices:\n  - name: a\n    depends_on: [ghost]\n",
			mentions: `depends_on "ghost" is not a service`,
		},
		{
			label:    "stop: leave, the key that was replaced",
			manifest: "name: p\nservices:\n  - name: mysql\n    cmd: brew services start mysql\n    port: 3306\n    stop: leave\n",
			mentions: "service mysql has a `stop:` key: it was replaced by the top-level `machine:` list",
		},
		{
			label:    "machine entry with no port",
			manifest: "name: p\nmachine:\n  - name: mysql\n",
			mentions: "machine mysql: port is required",
		},
		{
			label:    "machine entry with no name",
			manifest: "name: p\nmachine:\n  - port: 3306\n",
			mentions: "machine[0]: name is empty",
		},
		{
			label:    "machine entry named after a service",
			manifest: "name: p\nservices:\n  - name: api\nmachine:\n  - name: api\n    port: 3306\n",
			mentions: "machine api: api is already a service in this file",
		},
		{
			label:    "duplicate machine names",
			manifest: "name: p\nmachine:\n  - name: mysql\n    port: 3306\n  - name: mysql\n    port: 3307\n",
			mentions: "machine mysql: duplicate name",
		},
		{
			label:    "machine port out of range",
			manifest: "name: p\nmachine:\n  - name: mysql\n    port: 70000\n",
			mentions: "machine mysql: port 70000 is out of range",
		},
		{
			label:    "expose at the top level",
			manifest: "name: p\nexpose:\n  - 3000\n",
			mentions: "has an `expose:` key: sharing is not configured in",
		},
		{
			label:    "expose on a service",
			manifest: "name: p\nservices:\n  - name: api\n    expose: true\n",
			mentions: "service api has an `expose:` key: sharing is not configured in",
		},
		{
			label:    "share at the top level",
			manifest: "name: p\nshare:\n  - 3000\n",
			mentions: "has a `share:` key: sharing is not configured in",
		},
		{
			label:    "share on a service",
			manifest: "name: p\nservices:\n  - name: api\n    share: public\n",
			mentions: "service api has a `share:` key: sharing is not configured in",
		},
		{
			label:    "share on a later service",
			manifest: "name: p\nservices:\n  - name: web\n  - name: api\n    share: lan\n",
			mentions: "service api has a `share:` key",
		},
		{
			label:    "not yaml at all",
			manifest: "name: [unterminated\n",
			mentions: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			cfg, err := loadBody(t, "proj", tt.manifest)
			if err == nil {
				t.Fatalf("Load succeeded, want an error; got %+v", cfg)
			}
			if cfg != nil {
				t.Errorf("Load returned a config alongside its error: %+v", cfg)
			}
			if tt.mentions != "" && !strings.Contains(err.Error(), tt.mentions) {
				t.Errorf("error = %q, want it to mention %q", err, tt.mentions)
			}
			if !strings.Contains(err.Error(), ConfigName) {
				t.Errorf("error %q does not name the offending file", err)
			}
		})
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	_, err := loadBody(t, "proj", "name: bad name\nservices:\n  - name: a\n    port: 70000\n  - name: a\n")
	if err == nil {
		t.Fatal("want an error")
	}
	var configErr *ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("error is %T, want *ConfigError", err)
	}
	if len(configErr.Problems) < 3 {
		t.Errorf("problems = %v, want all three at once", configErr.Problems)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(tempTree(t), ConfigName)); err == nil {
		t.Fatal("a missing manifest loaded")
	}
}

// Metadata is free-form: read from the file, carried to the contract row, and
// never inferred by the engine.
func TestLoadServiceMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigName)
	writeFile(t, path, `name: demo-meta
services:
  - name: api
    cmd: uv run api
    port: 8100
    health: /health
    description: HTTP API service
    icon: globe
    color: "#123456"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	service := cfg.Services[0]
	if service.Description != "HTTP API service" || service.Icon != "globe" || service.Color != "#123456" {
		t.Fatalf("metadata not read: %+v", service)
	}

	row := ServiceRow(service)
	if row.Description == nil || *row.Description != service.Description {
		t.Fatalf("description not carried: %+v", row)
	}
	if row.Icon == nil || *row.Icon != service.Icon {
		t.Fatalf("icon not carried: %+v", row)
	}
	if row.Color == nil || *row.Color != service.Color {
		t.Fatalf("colour not carried: %+v", row)
	}
}

// Absent metadata stays null on the wire rather than becoming "".
func TestServiceRowLeavesMetadataNull(t *testing.T) {
	published := ServiceRow(Service{Name: "db"})
	if published.Description != nil {
		t.Fatalf("description = %v, want null", published.Description)
	}
	if published.Icon != nil || published.Color != nil || published.Health != nil {
		t.Fatalf("expected null metadata, got %+v", published)
	}
}

// worktree_ports is optional; absent is nil, not a zero that would read as
// "claim nothing".
func TestLoadWorktreePorts(t *testing.T) {
	loaded, err := loadBody(t, "shop", "name: shop\nworktree_ports: 5\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.WorktreePorts == nil || *loaded.WorktreePorts != 5 {
		t.Fatalf("worktree_ports = %v, want 5", loaded.WorktreePorts)
	}

	absent, err := loadBody(t, "plain", "name: plain\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if absent.WorktreePorts != nil {
		t.Fatalf("worktree_ports = %d, want nil", *absent.WorktreePorts)
	}

	for _, value := range []int{1, MaxWorktreePorts} {
		body := fmt.Sprintf("name: edge\nworktree_ports: %d\n", value)
		if _, err := loadBody(t, "edge", body); err != nil {
			t.Errorf("worktree_ports %d should be valid: %v", value, err)
		}
	}
}
