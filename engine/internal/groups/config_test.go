package groups

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func loadString(t *testing.T, dirName, body string) (*Config, error) {
	t.Helper()
	dir := mkdir(t, tempTree(t), dirName)
	path := filepath.Join(dir, ConfigName)
	writeFile(t, path, body)
	return Load(path)
}

func TestLoadValidConfig(t *testing.T) {
	cfg, err := loadString(t, "option-berth", `
name: option-berth
services:
  - name: db
    prepare: mvn -DskipTests package
    cmd: docker compose up db
    port: 5432
    health: /
  - name: api
    cmd: uv run uvicorn app:app
    cwd: backend
    port: 8000
    depends_on: [db]
ports: [9229]
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
	if len(cfg.Ports) != 1 || cfg.Ports[0] != 9229 {
		t.Errorf("ports = %v", cfg.Ports)
	}
}

func TestLoadDefaultsNameToDirectory(t *testing.T) {
	cfg, err := loadString(t, "storefront", "services: []\n")
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
	cfg, err := loadString(t, "shop", `
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
		name string
		body string
		want string
	}{
		{
			name: "name with a slash",
			body: "name: acme/web\n",
			want: `name "acme/web" contains "/"`,
		},
		{
			name: "name with whitespace",
			body: "name: my project\n",
			want: `contains " "`,
		},
		{
			name: "duplicate service names",
			body: "name: p\nservices:\n  - name: api\n  - name: api\n",
			want: "service api: duplicate name",
		},
		{
			name: "cwd escapes the config directory",
			body: "name: p\nservices:\n  - name: api\n    cwd: ../elsewhere\n",
			want: "escapes the directory",
		},
		{
			name: "absolute cwd",
			body: "name: p\nservices:\n  - name: api\n    cwd: /etc\n",
			want: "escapes the directory",
		},
		{
			name: "port out of range",
			body: "name: p\nservices:\n  - name: api\n    port: 70000\n",
			want: "port 70000 is out of range",
		},
		{
			name: "worktree_ports of zero",
			body: "name: p\nworktree_ports: 0\n",
			want: "worktree_ports: 0 is out of range 1-100",
		},
		{
			name: "worktree_ports above the cap",
			body: "name: p\nworktree_ports: 101\n",
			want: "worktree_ports: 101 is out of range 1-100",
		},
		{
			name: "worktree_ports that is not a number",
			body: "name: p\nworktree_ports: lots\n",
			want: "lots",
		},
		{
			name: "zero port in the ports list",
			body: "name: p\nports: [0]\n",
			want: "ports: 0 is out of range",
		},
		{
			name: "depends_on cycle",
			body: "name: p\nservices:\n  - name: a\n    depends_on: [b]\n  - name: b\n    depends_on: [a]\n",
			want: "depends_on has a cycle",
		},
		{
			name: "depends_on self cycle",
			body: "name: p\nservices:\n  - name: a\n    depends_on: [a]\n",
			want: "depends_on has a cycle",
		},
		{
			name: "depends_on unknown service",
			body: "name: p\nservices:\n  - name: a\n    depends_on: [ghost]\n",
			want: `depends_on "ghost" is not a service`,
		},
		{
			name: "stop: leave, the key that was replaced",
			body: "name: p\nservices:\n  - name: mysql\n    cmd: brew services start mysql\n    port: 3306\n    stop: leave\n",
			want: "service mysql has a `stop:` key: it was replaced by the top-level `machine:` list",
		},
		{
			name: "machine entry with no port",
			body: "name: p\nmachine:\n  - name: mysql\n",
			want: "machine mysql: port is required",
		},
		{
			name: "machine entry with no name",
			body: "name: p\nmachine:\n  - port: 3306\n",
			want: "machine[0]: name is empty",
		},
		{
			name: "machine entry named after a service",
			body: "name: p\nservices:\n  - name: api\nmachine:\n  - name: api\n    port: 3306\n",
			want: "machine api: api is already a service in this file",
		},
		{
			name: "duplicate machine names",
			body: "name: p\nmachine:\n  - name: mysql\n    port: 3306\n  - name: mysql\n    port: 3307\n",
			want: "machine mysql: duplicate name",
		},
		{
			name: "machine port out of range",
			body: "name: p\nmachine:\n  - name: mysql\n    port: 70000\n",
			want: "machine mysql: port 70000 is out of range",
		},
		{
			name: "expose at the top level",
			body: "name: p\nexpose:\n  - 3000\n",
			want: "has an `expose:` key: sharing is not configured in",
		},
		{
			name: "expose on a service",
			body: "name: p\nservices:\n  - name: api\n    expose: true\n",
			want: "service api has an `expose:` key: sharing is not configured in",
		},
		{
			name: "share at the top level",
			body: "name: p\nshare:\n  - 3000\n",
			want: "has a `share:` key: sharing is not configured in",
		},
		{
			name: "share on a service",
			body: "name: p\nservices:\n  - name: api\n    share: public\n",
			want: "service api has a `share:` key: sharing is not configured in",
		},
		{
			name: "share on a later service",
			body: "name: p\nservices:\n  - name: web\n  - name: api\n    share: lan\n",
			want: "service api has a `share:` key",
		},
		{
			name: "not yaml at all",
			body: "name: [unterminated\n",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadString(t, "proj", tt.body)
			if err == nil {
				t.Fatalf("Load succeeded, want an error; got %+v", cfg)
			}
			if cfg != nil {
				t.Errorf("Load returned a config alongside its error: %+v", cfg)
			}
			if tt.want != "" && !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), ConfigName) {
				t.Errorf("error %q does not name the offending file", err)
			}
		})
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	_, err := loadString(t, "proj", "name: bad name\nservices:\n  - name: a\n    port: 70000\n  - name: a\n")
	if err == nil {
		t.Fatal("want an error")
	}
	ce, ok := err.(*ConfigError)
	if !ok {
		t.Fatalf("error is %T, want *ConfigError", err)
	}
	if len(ce.Problems) < 3 {
		t.Errorf("problems = %v, want all three reported at once", ce.Problems)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(tempTree(t), ConfigName)); err == nil {
		t.Fatal("want an error for a missing file")
	}
}

// TestLoadServiceMetadata covers contract §13.1: description, icon and colour
// are free-form strings read from the file and never inferred.
func TestLoadServiceMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigName)
	writeFile(t, path, `name: demo
services:
  - name: api
    cmd: uv run api
    port: 8000
    health: /health
    description: The HTTP API
    icon: server
    color: "#3b82f6"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	svc := cfg.Services[0]
	if svc.Description != "The HTTP API" || svc.Icon != "server" || svc.Color != "#3b82f6" {
		t.Fatalf("metadata not read: %+v", svc)
	}

	row := ServiceRow(svc)
	if row.Description == nil || *row.Description != "The HTTP API" {
		t.Fatalf("description not carried to the contract row: %+v", row)
	}
	if row.Icon == nil || *row.Icon != "server" || row.Color == nil || *row.Color != "#3b82f6" {
		t.Fatalf("icon/colour not carried to the contract row: %+v", row)
	}
}

// TestServiceRowLeavesMetadataNull keeps "absent" distinct from "empty" on the
// wire: a service without metadata publishes null, not "".
func TestServiceRowLeavesMetadataNull(t *testing.T) {
	row := ServiceRow(Service{Name: "db"})
	if row.Description != nil || row.Icon != nil || row.Color != nil || row.Health != nil {
		t.Fatalf("expected null metadata, got %+v", row)
	}
}

// TestLoadWorktreePorts: the key is optional, and absent is nil rather than a
// zero that would read as "claim nothing".
func TestLoadWorktreePorts(t *testing.T) {
	cfg, err := loadString(t, "shop", "name: shop\nworktree_ports: 5\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.WorktreePorts == nil || *cfg.WorktreePorts != 5 {
		t.Fatalf("worktree_ports = %v, want 5", cfg.WorktreePorts)
	}

	cfg, err = loadString(t, "plain", "name: plain\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.WorktreePorts != nil {
		t.Fatalf("worktree_ports = %d, want nil for a file without the key", *cfg.WorktreePorts)
	}

	for _, n := range []int{1, MaxWorktreePorts} {
		if _, err := loadString(t, "edge", fmt.Sprintf("name: edge\nworktree_ports: %d\n", n)); err != nil {
			t.Errorf("worktree_ports %d should be valid: %v", n, err)
		}
	}
}
