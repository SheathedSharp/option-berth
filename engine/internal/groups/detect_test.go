package groups

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeAt(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func requireDeclared(t *testing.T, got, want []Declared) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Detect = %#v, want %#v", got, want)
	}
}

func TestDetectNodeScript(t *testing.T) {
	for _, tt := range []struct {
		name, scripts, lock, wantName, wantCmd, wantSource string
	}{
		{"dev with npm", `{"dev":"vite","build":"vite build"}`, "", "dev", "npm run dev", "package.json (scripts.dev)"},
		{"start when there is no dev", `{"start":"node server.js"}`, "", "start", "npm run start", "package.json (scripts.start)"},
		{"dev wins over start", `{"start":"node server.js","dev":"vite"}`, "", "dev", "npm run dev", "package.json (scripts.dev)"},
		{"pnpm from the lockfile", `{"dev":"vite"}`, "pnpm-lock.yaml", "dev", "pnpm run dev", "package.json (scripts.dev)"},
		{"yarn from the lockfile", `{"dev":"vite"}`, "yarn.lock", "dev", "yarn run dev", "package.json (scripts.dev)"},
		{"bun from the lockfile", `{"dev":"vite"}`, "bun.lock", "dev", "bun run dev", "package.json (scripts.dev)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeAt(t, dir, "package.json", `{"scripts":`+tt.scripts+`}`)
			if tt.lock != "" {
				writeAt(t, dir, tt.lock, "")
			}
			requireDeclared(t, Detect(dir), []Declared{{
				Name: tt.wantName, Cmd: tt.wantCmd, Source: tt.wantSource,
			}})
		})
	}
}

func TestDetectIgnoresAPackageWithNothingToRun(t *testing.T) {
	dir := t.TempDir()
	writeAt(t, dir, "package.json", `{"scripts":{"build":"tsc","test":"vitest"}}`)
	requireDeclared(t, Detect(dir), nil)
	writeAt(t, dir, "package.json", "not json at all")
	requireDeclared(t, Detect(dir), nil)
}

func TestDetectCompose(t *testing.T) {
	dir := t.TempDir()
	writeAt(t, dir, "compose.yaml", `
services:
  web:
    image: nginx
    ports: ["8080:80"]
    depends_on: [api]
  api:
    image: api
    ports:
      - "127.0.0.1:8000:8000"
  db:
    image: postgres
    ports:
      - target: 5432
        published: 5433
  worker:
    image: worker
  probe:
    image: probe
    ports: ["9000"]
  cache:
    image: redis
    ports: ["${CACHE_PORT:-6390}:6379"]
`)
	requireDeclared(t, Detect(dir), []Declared{
		{Name: "api", Cmd: "docker compose up api", Port: 8000, Source: "compose.yaml"},
		{Name: "cache", Cmd: "docker compose up cache", Port: 6390, Source: "compose.yaml"},
		{Name: "db", Cmd: "docker compose up db", Port: 5433, Source: "compose.yaml"},
		{Name: "probe", Cmd: "docker compose up probe", Source: "compose.yaml"},
		{Name: "web", Cmd: "docker compose up web", Port: 8080, Source: "compose.yaml"},
		{Name: "worker", Cmd: "docker compose up worker", Source: "compose.yaml"},
	})
}

func TestDetectPrefersTheComposeFileComposeWouldUse(t *testing.T) {
	dir := t.TempDir()
	writeAt(t, dir, "compose.yaml", "services:\n  first:\n    image: a\n")
	writeAt(t, dir, "docker-compose.yml", "services:\n  second:\n    image: b\n")
	requireDeclared(t, Detect(dir), []Declared{
		{Name: "first", Cmd: "docker compose up first", Source: "compose.yaml"},
	})
}

// A declaration is material for whoever writes the file, never a service: it
// shows up in the comment block with its source, and the list stays empty.
func TestDeclarationsAreCommentMaterial(t *testing.T) {
	dir := mkdir(t, tempTree(t), "shop")
	writeAt(t, dir, "compose.yaml", "services:\n  db:\n    image: postgres\n    ports: [\"5432:5432\"]\n")
	writeAt(t, dir, "package.json", `{"scripts":{"dev":"vite"}}`)

	declared := Detect(dir)
	requireDeclared(t, declared, []Declared{
		{Name: "dev", Cmd: "npm run dev", Source: "package.json (scripts.dev)"},
		{Name: "db", Cmd: "docker compose up db", Port: 5432, Source: "compose.yaml"},
	})

	cfg := Propose(dir, nil, nil)
	if cfg.Name != "shop" || cfg.Path != filepath.Join(dir, ConfigName) || cfg.Dir != dir {
		t.Fatalf("Propose = name %q dir %q path %q, want shop in %s", cfg.Name, cfg.Dir, cfg.Path, dir)
	}
	if cfg.Services != nil || cfg.Candidates != nil {
		t.Fatalf("Propose started with services %#v candidates %#v, want nil", cfg.Services, cfg.Candidates)
	}

	cfg.Declared = declared
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	parsed, err := Parse(filepath.Join(dir, ConfigName), data)
	if err != nil {
		t.Fatalf("the file does not load: %v\n%s", err, out)
	}
	if parsed.Name != "shop" || len(parsed.Services) != 0 || parsed.Declared != nil {
		t.Errorf("parsed name=%q services=%#v declared=%#v; want shop, no services, declarations not loaded",
			parsed.Name, parsed.Services, parsed.Declared)
	}
	for _, want := range []string{
		"name: shop\nservices: []\n",
		"# Stated by this project's own files. A command, but again not a declaration:\n# a compose service is one line of somebody else's file, and copying it up is\n# your call — `docker compose up <one service>` starts that service's\n# dependencies too, and those may be services you declare yourself.\n#\n#   dev  npm run dev  [package.json (scripts.dev)]\n#   db   docker compose up db  —  port 5432  [compose.yaml]\n#\n# Nothing was listening inside this project when this file was written.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing from the generated file:\n%s", want, out)
		}
	}
}

// TestUsesAssignedPorts: the CLI asks this before handing a file to a daemon,
// because one that predates the format parses it and blames the file.
func TestUsesAssignedPorts(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  *Config
		want bool
	}{
		{"a fixed port is understood by any daemon",
			&Config{Services: []Service{{Name: "db", Cmd: "postgres", Port: 5432}}}, false},
		{"an auto port is not", &Config{Services: []Service{{Name: "api", PortAuto: true}}}, true},
		{"an env block is not", &Config{Services: []Service{{Name: "api", Env: map[string]string{"A": "b"}}}}, true},
		{"a reference in cmd is not",
			&Config{Services: []Service{{Name: "api", Cmd: "api --port ${port}"}}}, true},
		{"a reference in prepare is not",
			&Config{Services: []Service{{Name: "api", Prepare: "build --port ${port}"}}}, true},
		{"an empty file is", &Config{}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.UsesAssignedPorts(); got != tt.want {
				t.Errorf("UsesAssignedPorts = %v, want %v", got, tt.want)
			}
		})
	}
}
