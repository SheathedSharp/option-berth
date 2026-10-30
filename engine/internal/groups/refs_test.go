package groups

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestExpand(t *testing.T) {
	values := Values{
		Self:     "api",
		Worktree: "wt_0123456789ab",
		Ports:    map[string]int{"api": 21408, "db": 21407},
	}
	for _, tt := range []struct{ in, want string }{
		{"--port ${port}", "--port 21408"},
		{"${url}/healthz", "http://localhost:21408/healthz"},
		{"postgres://localhost:${db.port}/app", "postgres://localhost:21407/app"},
		{"${db.url}", "http://localhost:21407"},
		{"app_${worktree}", "app_wt_0123456789ab"},
		{".data/${worktree}/app.db", ".data/wt_0123456789ab/app.db"},
		// Not references: left exactly as written.
		{"${HOME}/bin", "${HOME}/bin"},
		{"${FOO:-x}", "${FOO:-x}"},
		{"$port", "$port"},
		// A reference to a service with no port in the map stays put.
		{"${cache.port}", "${cache.port}"},
	} {
		if got := Expand(tt.in, values); got != tt.want {
			t.Errorf("Expand(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	// Without an identity the reference is left as written, like an unresolved
	// port reference: a daemon that does not know the checkout must not guess.
	unknown := Values{Self: "api", Ports: values.Ports}
	if got := Expand("app_${worktree}", unknown); got != "app_${worktree}" {
		t.Errorf("Expand with no identity = %q, want the reference unchanged", got)
	}
}

func TestWorktreeToken(t *testing.T) {
	dir := t.TempDir()
	token := WorktreeToken(dir)
	if token != WorktreeToken(dir) {
		t.Fatalf("WorktreeToken(%q) is not stable across calls", dir)
	}
	if !regexp.MustCompile(`^wt_[0-9a-f]{12}$`).MatchString(token) {
		t.Errorf("token = %q, want wt_ followed by 12 hex digits", token)
	}
	// The same directory reached through `..` names the same checkout.
	spelled := dir + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(dir)
	if got := WorktreeToken(spelled); got != token {
		t.Errorf("token through %q = %q, want %q", spelled, got, token)
	}
	if other := WorktreeToken(t.TempDir()); other == token {
		t.Errorf("two directories share the token %q", token)
	}
	if got := WorktreeToken(""); got != "" {
		t.Errorf("WorktreeToken(\"\") = %q, want empty", got)
	}
	if got := WorktreeTokenFor(dir); got != token {
		t.Errorf("WorktreeTokenFor(%q) = %q, want %q", dir, got, token)
	}
}

// TestWorktreeTokenPerCheckout: a nested manifest, the main checkout and a
// linked worktree each resolve to their own checkout's token. Two worktrees of
// one repository therefore never share a database name or data directory.
func TestWorktreeTokenPerCheckout(t *testing.T) {
	c := newCheckouts(t, "shop", "feat")
	nestedMain := mkdir(t, c.main, "api")
	nestedWorktree := mkdir(t, c.wt, "web")

	main := WorktreeTokenFor(nestedMain)
	worktree := WorktreeTokenFor(nestedWorktree)
	if main == worktree {
		t.Fatalf("main and linked worktree share the token %q", main)
	}
	if main != WorktreeTokenFor(c.main) {
		t.Errorf("a nested manifest has a different token than its main checkout")
	}
	if worktree != WorktreeTokenFor(c.wt) {
		t.Errorf("a nested manifest has a different token than its worktree")
	}
}

func TestRefs(t *testing.T) {
	got := Refs("x ${port} ${db.port} ${HOME} ${db.url} ${url} ${worktree}", "api")
	if strings.Join(got, ",") != "api,db" {
		t.Errorf("Refs = %v, want [api db]", got)
	}
}

func TestMatchesExpanded(t *testing.T) {
	for _, tt := range []struct {
		pattern, actual string
		want            bool
	}{
		{"--port=${port}", "--port=8123", true},
		{"--port=${port}", "--port=", false},
		{"${port}", "8123", true},
		{"--db=${worktree}", "--db=wt_0123456789ab", true},
		{"--db=${worktree}", "--db=", false},
		{"${HOME}", "${HOME}", true},
		{"${HOME}", "/Users/me", false},
		{"dev", "dev", true},
		{"dev", "build", false},
		{"a.b${port}", "axb1", false},
	} {
		if got := MatchesExpanded(tt.pattern, tt.actual); got != tt.want {
			t.Errorf("MatchesExpanded(%q, %q) = %v, want %v", tt.pattern, tt.actual, got, tt.want)
		}
	}
}

func TestLoadPortAutoAndEnv(t *testing.T) {
	cfg, err := loadString(t, "shop", `
name: shop
services:
  - name: db
    cmd: docker compose up db
    port: auto
  - name: api
    cmd: uv run uvicorn app:app --port ${port}
    port: auto
    depends_on: [db]
    env:
      DATABASE_URL: postgres://localhost:${db.port}/app
      HOME_BIN: ${HOME}/bin
  - name: worker
    cmd: uv run worker
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	api, _ := cfg.ServiceNamed("api")
	if !api.PortAuto || api.Port != 0 || !api.HasPort() {
		t.Errorf("api = %+v, want an auto port", api)
	}
	if api.Env["DATABASE_URL"] != "postgres://localhost:${db.port}/app" {
		t.Errorf("env = %v", api.Env)
	}
	if worker, _ := cfg.ServiceNamed("worker"); worker.HasPort() {
		t.Errorf("worker = %+v, want no port", worker)
	}

	out, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out), "port: auto") != 2 {
		t.Errorf("Marshal lost port: auto:\n%s", out)
	}
	back, err := Parse(cfg.Path, out)
	if err != nil {
		t.Fatalf("the marshalled file does not load: %v\n%s", err, out)
	}
	if db, _ := back.ServiceNamed("db"); !db.PortAuto {
		t.Errorf("round trip lost port: auto: %+v", db)
	}
}

// TestLoadAcceptsWorktreeReference: ${worktree} is a reference the daemon
// resolves from the checkout, not a service name, so a manifest may use it in
// cmd, prepare and env without declaring a service called "worktree".
func TestLoadAcceptsWorktreeReference(t *testing.T) {
	cfg, err := loadString(t, "demo", `
name: demo
services:
  - name: api
    prepare: build --data ${worktree}
    cmd: run --data ${worktree}
    port: auto
    env:
      DATA_DIR: .data/${worktree}
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	api, _ := cfg.ServiceNamed("api")
	if api.Env["DATA_DIR"] != ".data/${worktree}" {
		t.Errorf("env = %v, want the reference preserved for the daemon", api.Env)
	}
	if !cfg.UsesAssignedPorts() {
		t.Error("UsesAssignedPorts = false, want a file the assigned-port daemon must handle")
	}
}

func TestLoadReferenceProblems(t *testing.T) {
	for _, tt := range []struct{ name, body, want string }{
		{"a port that is neither", "services:\n  - name: api\n    port: fast\n",
			"port must be a number or auto"},
		{"an unknown service", "services:\n  - name: api\n    port: auto\n    cmd: run ${nope.port}\n",
			`names no service "nope"`},
		{"an unknown service in prepare", "services:\n  - name: api\n    port: auto\n    prepare: build --port ${nope.port}\n",
			`prepare ${nope.port} names no service "nope"`},
		{"a service with no port", "services:\n  - name: db\n  - name: api\n    env:\n      DB: ${db.port}\n",
			"needs a port, and db declares none"},
		{"its own port when it has none", "services:\n  - name: api\n    cmd: run --port ${port}\n",
			"needs a port, and api declares none"},
		{"a bad variable name", "services:\n  - name: api\n    env:\n      BAD-KEY: x\n",
			`env "BAD-KEY" is not a variable name`},
		{"a workspace reference in health is refused", "workspace:\n  - name: backend\n    worktree: ../backend\n    service: api\nservices:\n  - name: api\n    health:\n      command: curl -f ${backend.url}/health\n",
			"workspace addresses are expanded in prepare, cmd and env only"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadString(t, "demo", "name: demo\n"+tt.body)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestLoadAcceptsMachineAndWorkspaceReferences(t *testing.T) {
	cfg, err := loadString(t, "demo", `name: demo
machine:
  - name: mysql
    port: 3306
workspace:
  - name: backend
    worktree: ../backend
    service: api
services:
  - name: api
    cmd: run --db ${mysql.port} --backend ${backend.url}
    prepare: build --api ${backend.url}
    health:
      command: mysqladmin -h 127.0.0.1 -P ${mysql.port} ping
    env:
      API_URL: ${backend.url}
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Workspace) != 1 || cfg.Workspace[0].Name != "backend" {
		t.Fatalf("workspace = %+v", cfg.Workspace)
	}
	if !cfg.UsesManifestExtras() {
		t.Error("UsesManifestExtras = false, want true for a file with workspace references")
	}
	out, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := Parse(cfg.Path, out)
	if err != nil {
		t.Fatalf("the marshalled file does not load: %v\n%s", err, out)
	}
	if len(back.Workspace) != 1 || back.Workspace[0].Worktree != "../backend" {
		t.Errorf("round trip lost the workspace reference:\n%s", out)
	}
}

func TestWorkspaceValidationProblems(t *testing.T) {
	for _, tt := range []struct{ name, body, want string }{
		{"no name", "workspace:\n  - worktree: ../backend\n    service: api\n",
			"workspace[0]: name is empty"},
		{"no worktree", "workspace:\n  - name: backend\n    service: api\n",
			"workspace backend: worktree is required"},
		{"no service", "workspace:\n  - name: backend\n    worktree: ../backend\n",
			"workspace backend: service is required"},
		{"named after a service", "services:\n  - name: backend\nworkspace:\n  - name: backend\n    worktree: ../backend\n    service: api\n",
			"workspace backend: backend is already a service in this file"},
		{"named after a machine reference", "machine:\n  - name: backend\n    port: 3306\nworkspace:\n  - name: backend\n    worktree: ../backend\n    service: api\n",
			"workspace backend: backend is already a machine reference in this file"},
		{"duplicate names", "workspace:\n  - name: backend\n    worktree: ../a\n    service: api\n  - name: backend\n    worktree: ../b\n    service: api\n",
			"workspace backend: duplicate name"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadString(t, "demo", "name: demo\n"+tt.body)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
