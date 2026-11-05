package groups

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestExpandReferenceShapes(t *testing.T) {
	values := Values{
		Self:     "api",
		Worktree: "wt_0123456789ab",
		Ports:    map[string]int{"api": 21408, "db": 21407},
	}
	cases := []struct {
		label string
		input string
		want  string
	}{
		{"own port", "--port ${port}", "--port 21408"},
		{"own url", "${url}/healthz", "http://localhost:21408/healthz"},
		{"another service's port", "postgres://localhost:${db.port}/app", "postgres://localhost:21407/app"},
		{"another service's url", "${db.url}", "http://localhost:21407"},
		{"worktree token", "app_${worktree}", "app_wt_0123456789ab"},
		{"token inside a path", ".data/${worktree}/app.db", ".data/wt_0123456789ab/app.db"},

		// Not references: returned verbatim.
		{"shell variable", "${HOME}/bin", "${HOME}/bin"},
		{"shell default", "${FOO:-x}", "${FOO:-x}"},
		{"bare dollar", "$port", "$port"},

		// A service with no known port leaves its reference untouched.
		{"unknown port", "${cache.port}", "${cache.port}"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if got := Expand(tc.input, values); got != tc.want {
				t.Errorf("Expand(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}

	// Without an identity the worktree reference stays as written: a daemon
	// that does not know the checkout must not guess.
	unknown := Values{Self: "api", Ports: values.Ports}
	if got := Expand("app_${worktree}", unknown); got != "app_${worktree}" {
		t.Errorf("Expand with no identity = %q", got)
	}
}

func TestWorktreeTokenIsStableAndDistinct(t *testing.T) {
	dir := t.TempDir()
	token := WorktreeToken(dir)
	if again := WorktreeToken(dir); again != token {
		t.Fatalf("token changed between calls: %q then %q", token, again)
	}
	if !regexp.MustCompile(`^wt_[0-9a-f]{12}$`).MatchString(token) {
		t.Errorf("token = %q, want wt_ plus 12 hex digits", token)
	}

	// The same directory reached through `..` is the same checkout.
	spelled := dir + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(dir)
	if got := WorktreeToken(spelled); got != token {
		t.Errorf("token through %q = %q, want %q", spelled, got, token)
	}
	if other := WorktreeToken(t.TempDir()); other == token {
		t.Errorf("two directories share the token %q", token)
	}
	if empty := WorktreeToken(""); empty != "" {
		t.Errorf("WorktreeToken(\"\") = %q, want empty", empty)
	}
	if got := WorktreeTokenFor(dir); got != token {
		t.Errorf("WorktreeTokenFor(%q) = %q, want %q", dir, got, token)
	}
}

// A nested manifest, its main checkout and a linked worktree each resolve to
// their own checkout's token, so two worktrees never share a data directory.
func TestWorktreeTokenPerCheckout(t *testing.T) {
	checkouts := newCheckouts(t, "shop", "feat")
	nestedMain := mkdir(t, checkouts.main, "api")
	nestedWorktree := mkdir(t, checkouts.wt, "web")

	mainToken := WorktreeTokenFor(nestedMain)
	wtToken := WorktreeTokenFor(nestedWorktree)
	if mainToken == wtToken {
		t.Fatalf("main and worktree share the token %q", mainToken)
	}
	if mainToken != WorktreeTokenFor(checkouts.main) {
		t.Error("a nested manifest has a different token than its main checkout")
	}
	if wtToken != WorktreeTokenFor(checkouts.wt) {
		t.Error("a nested manifest has a different token than its worktree")
	}
}

func TestRefsListServicesOnly(t *testing.T) {
	names := Refs("x ${port} ${db.port} ${HOME} ${db.url} ${url} ${worktree}", "api")
	if joined := strings.Join(names, ","); joined != "api,db" {
		t.Errorf("Refs = %v, want [api db]", names)
	}
}

func TestMatchesExpandedReferencePattern(t *testing.T) {
	cases := []struct {
		label   string
		pattern string
		actual  string
		want    bool
	}{
		{"port with a value", "--port=${port}", "--port=8123", true},
		{"port with nothing", "--port=${port}", "--port=", false},
		{"whole argument", "${port}", "8123", true},
		{"worktree with a value", "--db=${worktree}", "--db=wt_0123456789ab", true},
		{"worktree with nothing", "--db=${worktree}", "--db=", false},
		{"literal shell variable", "${HOME}", "${HOME}", true},
		{"expanded shell variable", "${HOME}", "/Users/me", false},
		{"plain text", "dev", "dev", true},
		{"different plain text", "dev", "build", false},
		{"prefix must be exact", "a.b${port}", "axb1", false},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if got := MatchesExpanded(tc.pattern, tc.actual); got != tc.want {
				t.Errorf("MatchesExpanded(%q, %q) = %v, want %v",
					tc.pattern, tc.actual, got, tc.want)
			}
		})
	}
}

func TestLoadPortAutoAndEnv(t *testing.T) {
	cfg, err := loadString(t, "store", `
name: store
services:
  - name: postgres
    cmd: docker compose up postgres
    port: auto
  - name: web
    cmd: uv run uvicorn app:app --port ${port}
    port: auto
    depends_on: [postgres]
    env:
      DATABASE_URL: postgres://localhost:${postgres.port}/app
      PATH_HINT: ${HOME}/bin
  - name: jobs
    cmd: uv run worker
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	web, _ := cfg.ServiceNamed("web")
	if !web.PortAuto || web.Port != 0 || !web.HasPort() {
		t.Errorf("web = %+v, want an auto port", web)
	}
	if web.Env["DATABASE_URL"] != "postgres://localhost:${postgres.port}/app" {
		t.Errorf("env = %v", web.Env)
	}
	if jobs, _ := cfg.ServiceNamed("jobs"); jobs.HasPort() {
		t.Errorf("jobs = %+v, want no port", jobs)
	}

	rendered, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(rendered), "port: auto"); count != 2 {
		t.Errorf("Marshal wrote %d auto ports:\n%s", count, rendered)
	}
	reloaded, err := Parse(cfg.Path, rendered)
	if err != nil {
		t.Fatalf("the marshalled file does not load: %v\n%s", err, rendered)
	}
	if postgres, _ := reloaded.ServiceNamed("postgres"); !postgres.PortAuto {
		t.Errorf("round trip lost port: auto: %+v", postgres)
	}
}

// ${worktree} is resolved from the checkout, not a declared service, so a
// manifest may use it in cmd, prepare and env without declaring "worktree".
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
		t.Errorf("env = %v, want the reference preserved", api.Env)
	}
	if !cfg.UsesAssignedPorts() {
		t.Error("UsesAssignedPorts = false")
	}
}

func TestLoadReferenceProblems(t *testing.T) {
	cases := []struct {
		label    string
		manifest string
		mentions string
	}{
		{"neither a number nor auto", "services:\n  - name: api\n    port: fast\n",
			"port must be a number or auto"},
		{"unknown service in cmd", "services:\n  - name: api\n    port: auto\n    cmd: run ${nope.port}\n",
			`names no service "nope"`},
		{"unknown service in prepare", "services:\n  - name: api\n    port: auto\n    prepare: build --port ${nope.port}\n",
			`prepare ${nope.port} names no service "nope"`},
		{"dependency without a port", "services:\n  - name: db\n  - name: api\n    env:\n      DB: ${db.port}\n",
			"needs a port, and db declares none"},
		{"own port without a declaration", "services:\n  - name: api\n    cmd: run --port ${port}\n",
			"needs a port, and api declares none"},
		{"invalid env name", "services:\n  - name: api\n    env:\n      BAD-KEY: x\n",
			`env "BAD-KEY" is not a variable name`},
		{"workspace reference in health", "workspace:\n  - name: backend\n    worktree: ../backend\n    service: api\nservices:\n  - name: api\n    health:\n      command: curl -f ${backend.url}/health\n",
			"workspace addresses are expanded in prepare, cmd and env only"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			_, err := loadString(t, "demo", "name: demo\n"+tc.manifest)
			if err == nil || !strings.Contains(err.Error(), tc.mentions) {
				t.Fatalf("Load error = %v, want it to contain %q", err, tc.mentions)
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
		t.Error("UsesManifestExtras = false for a file with workspace references")
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
	cases := []struct {
		label    string
		manifest string
		mentions string
	}{
		{"missing name", "workspace:\n  - worktree: ../backend\n    service: api\n",
			"workspace[0]: name is empty"},
		{"missing worktree", "workspace:\n  - name: backend\n    service: api\n",
			"workspace backend: worktree is required"},
		{"missing service", "workspace:\n  - name: backend\n    worktree: ../backend\n",
			"workspace backend: service is required"},
		{"named after a service", "services:\n  - name: backend\nworkspace:\n  - name: backend\n    worktree: ../backend\n    service: api\n",
			"workspace backend: backend is already a service in this file"},
		{"named after a machine", "machine:\n  - name: backend\n    port: 3306\nworkspace:\n  - name: backend\n    worktree: ../backend\n    service: api\n",
			"workspace backend: backend is already a machine reference in this file"},
		{"duplicate names", "workspace:\n  - name: backend\n    worktree: ../a\n    service: api\n  - name: backend\n    worktree: ../b\n    service: api\n",
			"workspace backend: duplicate name"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			_, err := loadString(t, "demo", "name: demo\n"+tc.manifest)
			if err == nil || !strings.Contains(err.Error(), tc.mentions) {
				t.Fatalf("Load error = %v, want it to contain %q", err, tc.mentions)
			}
		})
	}
}
