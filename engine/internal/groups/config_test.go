package groups

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func mustLoad(t *testing.T, dirName, body string) *Config {
	t.Helper()
	cfg, err := loadString(t, dirName, body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load accepted a document but returned no config")
	}
	return cfg
}

func refuseLoad(t *testing.T, dirName, body string) error {
	t.Helper()
	cfg, err := loadString(t, dirName, body)
	if err == nil {
		t.Fatal("Load accepted a document that must be refused")
	}
	if cfg != nil {
		t.Errorf("Load returned a config alongside %v", err)
	}
	if !strings.Contains(err.Error(), ConfigName) {
		t.Errorf("error %q does not name %s", err, ConfigName)
	}
	return err
}

func requireConfigProblems(t *testing.T, err error, want ...string) {
	t.Helper()
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("got %T %v, want *ConfigError", err, err)
	}
	if !strings.Contains(ce.Path, ConfigName) {
		t.Errorf("ConfigError.Path %q does not name %s", ce.Path, ConfigName)
	}
	if !reflect.DeepEqual(ce.Problems, want) {
		t.Errorf("Problems = %#v, want %#v", ce.Problems, want)
	}
	for _, problem := range want {
		if !strings.Contains(ce.Error(), problem) {
			t.Errorf("Error() %q does not include problem %q", ce.Error(), problem)
		}
	}
}

func requirePlainError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	var ce *ConfigError
	if errors.As(err, &ce) {
		t.Fatalf("got *ConfigError %v, want a plain error %q", ce, want)
	}
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestLoadValidConfig(t *testing.T) {
	cfg := mustLoad(t, "option-berth", `name: option-berth
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
	if cfg.Name != "option-berth" {
		t.Errorf("Name = %q, want option-berth", cfg.Name)
	}
	if len(cfg.Services) != 2 {
		t.Fatalf("len(Services) = %d, want 2", len(cfg.Services))
	}
	db, api := cfg.Services[0], cfg.Services[1]
	if db.Name != "db" || db.Cmd != "docker compose up db" || db.Port != 5432 || db.Health != "/" {
		t.Errorf("first service = %+v, want db on 5432 with health /", db)
	}
	if db.Prepare != "mvn -DskipTests package" {
		t.Errorf("Services[0].Prepare = %q, want the written Maven package command", db.Prepare)
	}
	if api.Name != "api" || api.Cmd != "uv run uvicorn app:app" || api.Cwd != "backend" || api.Port != 8000 {
		t.Errorf("second service = %+v, want api in backend on 8000", api)
	}
	if !reflect.DeepEqual(api.DependsOn, []string{"db"}) {
		t.Errorf("api.DependsOn = %v, want [db]", api.DependsOn)
	}
	if got := cfg.ServiceDir(api); got != filepath.Join(cfg.Dir, "backend") {
		t.Errorf("ServiceDir(api) = %q, want %s/backend", got, cfg.Dir)
	}
	if !reflect.DeepEqual(cfg.Ports, []int{9229}) {
		t.Errorf("Ports = %v, want [9229]", cfg.Ports)
	}
}

func TestLoadDefaultsNameToDirectory(t *testing.T) {
	cfg := mustLoad(t, "storefront", "services: []\n")
	if cfg.Name != "storefront" {
		t.Errorf("Name = %q, want the directory name storefront", cfg.Name)
	}
}

func TestManifestDocumentEdges(t *testing.T) {
	dir := mkdir(t, tempTree(t), "edges")
	path := filepath.Join(dir, ConfigName)

	cfg, err := Parse(path, nil)
	if err != nil {
		t.Fatalf("Parse(empty): %v", err)
	}
	if cfg.Name != "edges" {
		t.Errorf("empty document name = %q, want edges", cfg.Name)
	}
	if len(cfg.Services) != 0 {
		t.Errorf("empty document services = %d, want none", len(cfg.Services))
	}

	_, err = Parse(path, []byte("name: a\n---\nname: b\n"))
	if err == nil {
		t.Fatal("Parse accepted a second YAML document")
	}
	requireConfigProblems(t, err, "expected a single YAML document")

	_, err = Parse("", nil)
	requirePlainError(t, err, "no config path given")
	_, err = Load("")
	requirePlainError(t, err, "no config path given")
}

func TestDuplicateServiceKeysAreRefused(t *testing.T) {
	const path = "/tmp/p/oberth.yaml"
	cases := []struct {
		key      string
		body     string
		line, at int
	}{
		{"port", "name: p\nservices:\n  - name: a\n    port: 1\n    port: 2\n", 5, 4},
		{"health", "name: p\nservices:\n  - name: a\n    health: /x\n    health: /y\n", 5, 4},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			_, err := Parse(path, []byte(c.body))
			if err == nil {
				t.Fatal("Parse accepted a duplicate mapping key")
			}
			requireConfigProblems(t, err, fmt.Sprintf("line %d: mapping key %q already defined at line %d", c.line, c.key, c.at))
		})
	}
}

func TestRetiredKeyOnAnUnnamedService(t *testing.T) {
	_, err := Parse("/tmp/p/oberth.yaml", []byte("name: p\nservices:\n  - stop: mysql\n"))
	if err == nil {
		t.Fatal("Parse accepted a retired stop: key on an unnamed service")
	}
	requireConfigProblems(t, err, "oberth.yaml has a `stop:` key: it was replaced by the top-level `machine:` list. A service the machine owns is a reference now — one entry per service, `- name: mysql` with `port: 3306` — and `up` does not start it either")
}

func TestMachineReferences(t *testing.T) {
	cfg := mustLoad(t, "proj", `name: p
services:
  - name: api
    cmd: go run .
    port: 8000
machine:
  - name: mysql
    port: 3306
  - name: redis
    port: 6379
`)
	if len(cfg.Services) != 1 || cfg.Services[0].Name != "api" {
		t.Errorf("Services = %v, want only api; machine entries must not leak into the service list", serviceNames(cfg.Services))
	}
	if len(cfg.Machine) != 2 {
		t.Fatalf("len(Machine) = %d, want 2", len(cfg.Machine))
	}
	if cfg.Machine[0] != (MachineRef{Name: "mysql", Port: 3306}) {
		t.Errorf("Machine[0] = %+v, want mysql:3306", cfg.Machine[0])
	}
	if cfg.Machine[1] != (MachineRef{Name: "redis", Port: 6379}) {
		t.Errorf("Machine[1] = %+v, want redis:6379", cfg.Machine[1])
	}

	data, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	round, err := Parse(filepath.Join(cfg.Dir, ConfigName), data)
	if err != nil {
		t.Fatalf("Parse(Marshal): %v", err)
	}
	if !reflect.DeepEqual(round.Machine, cfg.Machine) {
		t.Errorf("round-trip Machine = %+v, want %+v", round.Machine, cfg.Machine)
	}
}

func serviceNames(services []Service) []string {
	names := make([]string, len(services))
	for i, service := range services {
		names[i] = service.Name
	}
	return names
}

func TestLoadValidationProblems(t *testing.T) {
	cases := []struct {
		name, body string
		problems   []string
	}{
		{
			name:     "name with a slash",
			body:     "name: acme/web\n",
			problems: []string{`name "acme/web" contains "/"; names may not contain slashes or whitespace`},
		},
		{
			name:     "name with whitespace",
			body:     "name: my project\n",
			problems: []string{`name "my project" contains " "; names may not contain slashes or whitespace`},
		},
		{
			name:     "duplicate service names",
			body:     "name: p\nservices:\n  - name: api\n  - name: api\n",
			problems: []string{"service api: duplicate name"},
		},
		{
			name:     "cwd escapes the config directory",
			body:     "name: p\nservices:\n  - name: api\n    cwd: ../elsewhere\n",
			problems: []string{`service api: cwd "../elsewhere" escapes the directory holding oberth.yaml`},
		},
		{
			name:     "absolute cwd",
			body:     "name: p\nservices:\n  - name: api\n    cwd: /etc\n",
			problems: []string{`service api: cwd "/etc" escapes the directory holding oberth.yaml`},
		},
		{
			name:     "port out of range",
			body:     "name: p\nservices:\n  - name: api\n    port: 70000\n",
			problems: []string{"service api: port 70000 is out of range 1-65535"},
		},
		{
			name:     "worktree_ports of zero",
			body:     "name: p\nworktree_ports: 0\n",
			problems: []string{"worktree_ports: 0 is out of range 1-100"},
		},
		{
			name:     "worktree_ports above the cap",
			body:     "name: p\nworktree_ports: 101\n",
			problems: []string{"worktree_ports: 101 is out of range 1-100"},
		},
		{
			name: "worktree_ports that is not a number",
			body: "name: p\nworktree_ports: lots\n",
			problems: []string{
				"yaml: unmarshal errors:\n  line 2: cannot unmarshal !!str `lots` into int",
			},
		},
		{
			name:     "zero port in the ports list",
			body:     "name: p\nports: [0]\n",
			problems: []string{"ports: 0 is out of range 1-65535"},
		},
		{
			name:     "depends_on cycle",
			body:     "name: p\nservices:\n  - name: a\n    depends_on: [b]\n  - name: b\n    depends_on: [a]\n",
			problems: []string{"depends_on has a cycle: a -> b -> a"},
		},
		{
			name:     "depends_on self cycle",
			body:     "name: p\nservices:\n  - name: a\n    depends_on: [a]\n",
			problems: []string{"depends_on has a cycle: a -> a"},
		},
		{
			name:     "depends_on unknown service",
			body:     "name: p\nservices:\n  - name: a\n    depends_on: [ghost]\n",
			problems: []string{`service a: depends_on "ghost" is not a service in this file`},
		},
		{
			name: "stop: leave, the key that was replaced",
			body: "name: p\nservices:\n  - name: mysql\n    cmd: brew services start mysql\n    port: 3306\n    stop: leave\n",
			problems: []string{
				"service mysql has a `stop:` key: it was replaced by the top-level `machine:` list. A service the machine owns is a reference now — one entry per service, `- name: mysql` with `port: 3306` — and `up` does not start it either",
			},
		},
		{
			name:     "machine entry with no port",
			body:     "name: p\nmachine:\n  - name: mysql\n",
			problems: []string{"machine mysql: port is required — it is what the reference is matched by"},
		},
		{
			name:     "machine entry with no name",
			body:     "name: p\nmachine:\n  - port: 3306\n",
			problems: []string{"machine[0]: name is empty"},
		},
		{
			name:     "machine entry named after a service",
			body:     "name: p\nservices:\n  - name: api\nmachine:\n  - name: api\n    port: 3306\n",
			problems: []string{"machine api: api is already a service in this file"},
		},
		{
			name:     "duplicate machine names",
			body:     "name: p\nmachine:\n  - name: mysql\n    port: 3306\n  - name: mysql\n    port: 3307\n",
			problems: []string{"machine mysql: duplicate name"},
		},
		{
			name:     "machine port out of range",
			body:     "name: p\nmachine:\n  - name: mysql\n    port: 70000\n",
			problems: []string{"machine mysql: port 70000 is out of range 1-65535"},
		},
		{
			name:     "expose at the top level",
			body:     "name: p\nexpose:\n  - 3000\n",
			problems: []string{"oberth.yaml has an `expose:` key: sharing is not configured in oberth.yaml. Remote hosts and public sharing are outside what this engine does"},
		},
		{
			name:     "expose on a service",
			body:     "name: p\nservices:\n  - name: api\n    expose: true\n",
			problems: []string{"service api has an `expose:` key: sharing is not configured in oberth.yaml. Remote hosts and public sharing are outside what this engine does"},
		},
		{
			name:     "share at the top level",
			body:     "name: p\nshare:\n  - 3000\n",
			problems: []string{"oberth.yaml has a `share:` key: sharing is not configured in oberth.yaml. Remote hosts and public sharing are outside what this engine does"},
		},
		{
			name:     "share on a service",
			body:     "name: p\nservices:\n  - name: api\n    share: public\n",
			problems: []string{"service api has a `share:` key: sharing is not configured in oberth.yaml. Remote hosts and public sharing are outside what this engine does"},
		},
		{
			name:     "share on a later service",
			body:     "name: p\nservices:\n  - name: web\n  - name: api\n    share: lan\n",
			problems: []string{"service api has a `share:` key: sharing is not configured in oberth.yaml. Remote hosts and public sharing are outside what this engine does"},
		},
		{
			name:     "not yaml at all",
			body:     "name: [unterminated\n",
			problems: []string{"yaml: line 1: did not find expected ',' or ']'"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			requireConfigProblems(t, refuseLoad(t, "proj", c.body), c.problems...)
		})
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	err := refuseLoad(t, "proj", `name: bad name
services:
  - name: a
    port: 70000
  - name: a
`)
	requireConfigProblems(t, err,
		`name "bad name" contains " "; names may not contain slashes or whitespace`,
		"service a: duplicate name",
		"service a: port 70000 is out of range 1-65535",
	)
}

func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(tempTree(t), ConfigName)
	cfg, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted a missing file")
	}
	if cfg != nil {
		t.Errorf("Load returned a config for a missing file")
	}
	var ce *ConfigError
	if errors.As(err, &ce) {
		t.Fatalf("missing file was wrapped as *ConfigError %v, want a filesystem error", ce)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error %v is not os.ErrNotExist", err)
	}
	if !strings.Contains(err.Error(), ConfigName) {
		t.Errorf("error %q does not name %s", err, ConfigName)
	}
}

func TestLoadServiceMetadata(t *testing.T) {
	cfg := mustLoad(t, "demo", `name: demo
services:
  - name: api
    cmd: uv run api
    port: 8000
    health: /health
    description: The HTTP API
    icon: server
    color: "#3b82f6"
`)
	svc := cfg.Services[0]
	if svc.Description != "The HTTP API" || svc.Icon != "server" || svc.Color != "#3b82f6" {
		t.Errorf("metadata = %+v, want description/icon/color as written", svc)
	}
	row := ServiceRow(svc)
	if row.Description == nil || *row.Description != "The HTTP API" {
		t.Errorf("ServiceRow.Description = %v, want pointer to The HTTP API", row.Description)
	}
	if row.Icon == nil || *row.Icon != "server" {
		t.Errorf("ServiceRow.Icon = %v, want pointer to server", row.Icon)
	}
	if row.Color == nil || *row.Color != "#3b82f6" {
		t.Errorf("ServiceRow.Color = %v, want pointer to #3b82f6", row.Color)
	}
}

func TestServiceRowLeavesMetadataNull(t *testing.T) {
	row := ServiceRow(Service{Name: "db"})
	if row.Description != nil || row.Icon != nil || row.Color != nil || row.Health != nil {
		t.Errorf("expected null metadata pointers, got %+v", row)
	}
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"description", "icon", "color", "health"} {
		if raw[key] != nil {
			t.Errorf("%s = %#v, want null", key, raw[key])
		}
	}
}

func TestLoadWorktreePorts(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		cfg := mustLoad(t, "shop", "name: shop\nworktree_ports: 5\n")
		if cfg.WorktreePorts == nil || *cfg.WorktreePorts != 5 {
			t.Errorf("WorktreePorts = %v, want pointer to 5", cfg.WorktreePorts)
		}
	})
	t.Run("absent", func(t *testing.T) {
		cfg := mustLoad(t, "plain", "name: plain\n")
		if cfg.WorktreePorts != nil {
			t.Errorf("WorktreePorts = %v, want nil when the key is omitted", cfg.WorktreePorts)
		}
	})
	t.Run("bounds", func(t *testing.T) {
		for _, n := range []int{1, MaxWorktreePorts} {
			cfg := mustLoad(t, "edge", fmt.Sprintf("name: edge\nworktree_ports: %d\n", n))
			if cfg.WorktreePorts == nil || *cfg.WorktreePorts != n {
				t.Errorf("WorktreePorts = %v, want pointer to %d", cfg.WorktreePorts, n)
			}
		}
	})
}
