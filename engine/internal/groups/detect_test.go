package groups

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAt(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func declaredNames(declared []Declared) string {
	out := make([]string, 0, len(declared))
	for _, d := range declared {
		out = append(out, d.Name)
	}
	return strings.Join(out, ",")
}

func TestDetectNodeScript(t *testing.T) {
	for _, tt := range []struct {
		name, scripts, lock, wantCmd string
	}{
		{"dev with npm", `{"dev":"vite","build":"vite build"}`, "", "npm run dev"},
		{"start when there is no dev", `{"start":"node server.js"}`, "", "npm run start"},
		{"dev wins over start", `{"start":"node server.js","dev":"vite"}`, "", "npm run dev"},
		{"pnpm from the lockfile", `{"dev":"vite"}`, "pnpm-lock.yaml", "pnpm run dev"},
		{"yarn from the lockfile", `{"dev":"vite"}`, "yarn.lock", "yarn run dev"},
		{"bun from the lockfile", `{"dev":"vite"}`, "bun.lock", "bun run dev"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeAt(t, dir, "package.json", `{"scripts":`+tt.scripts+`}`)
			if tt.lock != "" {
				writeAt(t, dir, tt.lock, "")
			}
			got := Detect(dir)
			if len(got) != 1 || got[0].Cmd != tt.wantCmd {
				t.Fatalf("Detect = %+v, want one declaration running %q", got, tt.wantCmd)
			}
			// The comment that carries this into the file has to say where it
			// came from: a command with no source is one a reader cannot check.
			if !strings.Contains(got[0].Source, "package.json") {
				t.Errorf("source = %q, want the file it was read from", got[0].Source)
			}
		})
	}
}

func TestDetectIgnoresAPackageWithNothingToRun(t *testing.T) {
	dir := t.TempDir()
	writeAt(t, dir, "package.json", `{"scripts":{"build":"tsc","test":"vitest"}}`)
	if got := Detect(dir); len(got) != 0 {
		t.Errorf("Detect = %+v, want nothing: neither script runs the project", got)
	}
	writeAt(t, dir, "package.json", "not json at all")
	if got := Detect(dir); len(got) != 0 {
		t.Errorf("Detect = %+v, want nothing from a broken package.json", got)
	}
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
	got := Detect(dir)
	if declaredNames(got) != "api,cache,db,probe,web,worker" {
		t.Fatalf("Detect = %s, want every compose service in name order", declaredNames(got))
	}
	byName := map[string]Declared{}
	for _, d := range got {
		byName[d.Name] = d
	}
	if d := byName["web"]; d.Port != 8080 || d.Cmd != "docker compose up web" {
		t.Errorf("web = %+v", d)
	}
	if d := byName["api"]; d.Port != 8000 {
		t.Errorf("api port = %d, want the host side of a three-part mapping", d.Port)
	}
	if d := byName["db"]; d.Port != 5433 {
		t.Errorf("db port = %d, want the long syntax's published port", d.Port)
	}
	if d := byName["probe"]; d.Port != 0 {
		t.Errorf("probe port = %d: a bare container port publishes nothing fixed", d.Port)
	}
	// `${VAR:-default}` is how most compose files spell a configurable port.
	// Reading the colon inside it as the mapping's separator used to lose the
	// port entirely, and a service with no port reads as "never running".
	if d := byName["cache"]; d.Port != 6390 {
		t.Errorf("cache port = %d, want the default from ${CACHE_PORT:-6390}", d.Port)
	}
	if d := byName["worker"]; d.Port != 0 || d.Cmd != "docker compose up worker" {
		t.Errorf("worker = %+v, want a declaration with no port", d)
	}
	if d := byName["web"]; d.Source != "compose.yaml" {
		t.Errorf("source = %q, want the file the declaration was read from", d.Source)
	}
}

func TestDetectPrefersTheComposeFileComposeWouldUse(t *testing.T) {
	dir := t.TempDir()
	writeAt(t, dir, "compose.yaml", "services:\n  first:\n    image: a\n")
	writeAt(t, dir, "docker-compose.yml", "services:\n  second:\n    image: b\n")
	if got := Detect(dir); declaredNames(got) != "first" {
		t.Errorf("Detect = %s, want compose.yaml to win", declaredNames(got))
	}
}

// A declaration is material for whoever writes the file, never a service: it
// shows up in the comment block with its source, and the list stays empty.
func TestDeclarationsAreCommentMaterial(t *testing.T) {
	dir := mkdir(t, tempTree(t), "shop")
	writeAt(t, dir, "compose.yaml", "services:\n  db:\n    image: postgres\n    ports: [\"5432:5432\"]\n")
	writeAt(t, dir, "package.json", `{"scripts":{"dev":"vite"}}`)

	cfg := Propose(dir, nil, nil)
	cfg.Declared = Detect(dir)
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	parsed, err := Parse(filepath.Join(dir, ConfigName), data)
	if err != nil {
		t.Fatalf("the file does not load: %v\n%s", err, out)
	}
	if len(parsed.Services) != 0 {
		t.Errorf("services = %+v, want none: nothing but a person writes the list", parsed.Services)
	}
	for _, want := range []string{
		"#   dev  npm run dev  [package.json (scripts.dev)]",
		"#   db   docker compose up db  —  port 5432  [compose.yaml]",
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
