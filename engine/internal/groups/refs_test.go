package groups

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExpand(t *testing.T) {
	values := Values{
		Self:     "api",
		Worktree: "wt_0123456789ab",
		Ports:    map[string]int{"api": 21408, "db": 21407},
	}
	cases := []struct{ in, want string }{
		{"--port ${port}", "--port 21408"},
		{"${url}/healthz", "http://localhost:21408/healthz"},
		{"postgres://localhost:${db.port}/app", "postgres://localhost:21407/app"},
		{"${db.url}", "http://localhost:21407"},
		{"app_${worktree}", "app_wt_0123456789ab"},
		{".data/${worktree}/app.db", ".data/wt_0123456789ab/app.db"},
		// Shell syntax is not one of our references and comes back untouched.
		{"${HOME}/bin", "${HOME}/bin"},
		{"${FOO:-x}", "${FOO:-x}"},
		{"$port", "$port"},
		// A service with no port in the map leaves its reference alone.
		{"${cache.port}", "${cache.port}"},
	}
	for _, tc := range cases {
		if got := Expand(tc.in, values); got != tc.want {
			t.Errorf("Expand(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := URL(21408); got != "http://localhost:21408" {
		t.Errorf("URL(21408) = %q, want http://localhost:21408", got)
	}
	// Without an identity the reference is left as written, like an unresolved
	// port reference: a runtime service that does not know the checkout must not guess.
	unknown := Values{Self: "api", Ports: values.Ports}
	if got := Expand("app_${worktree}", unknown); got != "app_${worktree}" {
		t.Errorf("Expand with no identity = %q, want the reference unchanged", got)
	}
}

// The reference vocabulary is closed: only ${port}, ${url}, ${service.port}
// and ${service.url} are service addresses. ${worktree} is an identity, not a
// service, and everything else is text.
func TestReferenceParsingIsAClosedSet(t *testing.T) {
	for _, tt := range []struct {
		inner string
		want  reference
		ok    bool
	}{
		{"port", reference{field: fieldPort}, true},
		{"url", reference{field: fieldURL}, true},
		{"api.port", reference{service: "api", field: fieldPort}, true},
		{"api.url", reference{service: "api", field: fieldURL}, true},
		{"a.b.url", reference{service: "a.b", field: fieldURL}, true},
		{"worktree", reference{}, false},
		{"HOME", reference{}, false},
		{".port", reference{}, false},
		{"port.", reference{}, false},
		{"api.health", reference{}, false},
	} {
		got, ok := parseReference(tt.inner)
		if got != tt.want || ok != tt.ok {
			t.Errorf("parseReference(%q) = %+v, %v; want %+v, %v", tt.inner, got, ok, tt.want, tt.ok)
		}
		if isReference(tt.inner) != (tt.ok || tt.inner == "worktree") {
			t.Errorf("isReference(%q) = %v, want accepted only for service addresses and worktree", tt.inner, isReference(tt.inner))
		}
	}
}

func TestWorktreeToken(t *testing.T) {
	dir := t.TempDir()
	token := WorktreeToken(dir)
	requireWorktreeToken(t, dir, token)
	if token != WorktreeToken(dir) {
		t.Fatalf("WorktreeToken(%q) is not stable across calls", dir)
	}
	// The same directory reached through `..` names the same checkout.
	spelled := dir + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(dir)
	if got := WorktreeToken(spelled); got != token {
		t.Errorf("token through %q = %q, want %q", spelled, got, token)
	}
	requireWorktreeToken(t, spelled, WorktreeToken(spelled))
	if other := WorktreeToken(t.TempDir()); other == token {
		t.Errorf("two directories share the token %q", token)
	}
	if got := WorktreeToken(""); got != "" {
		t.Errorf("WorktreeToken(\"\") = %q, want empty", got)
	}
	if got := WorktreeTokenFor(dir); got != token {
		t.Errorf("WorktreeTokenFor(%q) = %q, want %q", dir, got, token)
	}
	identity, root := WorktreeIdentityFor(dir)
	if identity != token || root != Canonical(dir) {
		t.Errorf("WorktreeIdentityFor(%q) = %q, %q; want %q, %q", dir, identity, root, token, Canonical(dir))
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
	requireWorktreeToken(t, c.main, main)
	requireWorktreeToken(t, c.wt, worktree)
	if main == worktree {
		t.Fatalf("main and linked worktree share the token %q", main)
	}
	if main != WorktreeTokenFor(c.main) || main != WorktreeToken(c.main) {
		t.Errorf("a nested manifest has a different token than its main checkout: nested=%q root=%q token=%q", main, WorktreeTokenFor(c.main), WorktreeToken(c.main))
	}
	if worktree != WorktreeTokenFor(c.wt) || worktree != WorktreeToken(c.wt) {
		t.Errorf("a nested manifest has a different token than its worktree: nested=%q root=%q token=%q", worktree, WorktreeTokenFor(c.wt), WorktreeToken(c.wt))
	}
}

func TestRefs(t *testing.T) {
	got := Refs("x ${port} ${db.port} ${HOME} ${db.url} ${url} ${worktree}", "api")
	if !reflect.DeepEqual(got, []string{"api", "db"}) {
		t.Errorf("Refs = %#v, want []string{\"api\", \"db\"}", got)
	}
	for _, text := range []string{"", "app_${worktree}", "${HOME}"} {
		if got := Refs(text, "api"); got != nil {
			t.Errorf("Refs(%q) = %#v, want nil (no service names)", text, got)
		}
	}
}

func TestMatchesExpanded(t *testing.T) {
	type matchCase struct {
		pattern, actual string
		want            bool
	}
	cases := []matchCase{
		{"--port=${port}", "--port=8123", true},
		{"--port=${port}", "--port=", false},
		{"${port}", "8123", true},
		{"--db=${worktree}", "--db=wt_0123456789ab", true},
		{"--db=${worktree}", "--db=", false},
		{"${HOME}", "${HOME}", true},
		{"${HOME}", "/Users/me", false},
		{"seed", "seed", true},
		{"seed", "build", false},
		{"a.b${port}", "axb1", false},
	}
	for _, tc := range cases {
		if got := MatchesExpanded(tc.pattern, tc.actual); got != tc.want {
			t.Errorf("MatchesExpanded(%q, %q) = %v, want %v", tc.pattern, tc.actual, got, tc.want)
		}
	}
}

func TestLoadPortAutoAndEnv(t *testing.T) {
	cfg := mustLoad(t, "shop", `
name: shop
services:
  - name: store
    cmd: docker compose up store
    port: auto
  - name: app
    cmd: uv run uvicorn app.main:app --port ${port}
    port: auto
    depends_on: [store]
    env:
      DATABASE_URL: postgres://localhost:${store.port}/app
      HOME_BIN: ${HOME}/bin
  - name: job
    cmd: uv run worker
`)
	if cfg.Name != "shop" {
		t.Errorf("Name = %q, want shop", cfg.Name)
	}
	requireNamedService(t, cfg, Service{Name: "store", Cmd: "docker compose up store", PortAuto: true})
	requireNamedService(t, cfg, Service{
		Name: "app", Cmd: "uv run uvicorn app.main:app --port ${port}", PortAuto: true,
		DependsOn: []string{"store"},
		Env:       map[string]string{"DATABASE_URL": "postgres://localhost:${store.port}/app", "HOME_BIN": "${HOME}/bin"},
	})
	requireNamedService(t, cfg, Service{Name: "job", Cmd: "uv run worker"})
	if !cfg.UsesAssignedPorts() || cfg.UsesManifestExtras() {
		t.Errorf("UsesAssignedPorts=%v UsesManifestExtras=%v, want true, false", cfg.UsesAssignedPorts(), cfg.UsesManifestExtras())
	}
	requireMarshalDeclares(t, cfg, ""+
		"name: shop\n"+
		"services:\n"+
		"  - name: store\n"+
		"    cmd: docker compose up store\n"+
		"    port: auto\n"+
		"  - name: app\n"+
		"    cmd: uv run uvicorn app.main:app --port ${port}\n"+
		"    port: auto\n"+
		"    depends_on:\n"+
		"      - store\n"+
		"    env:\n"+
		"      DATABASE_URL: postgres://localhost:${store.port}/app\n"+
		"      HOME_BIN: ${HOME}/bin\n"+
		"  - name: job\n"+
		"    cmd: uv run worker\n")
}

// TestLoadAcceptsWorktreeReference: ${worktree} is a reference the runtime service
// resolves from the checkout, not a service name, so a manifest may use it in
// cmd, prepare and env without declaring a service called "worktree".
func TestLoadAcceptsWorktreeReference(t *testing.T) {
	cfg := mustLoad(t, "demo", `
name: demo
services:
  - name: api
    prepare: build --data ${worktree}
    cmd: run --data ${worktree}
    port: auto
    env:
      DATA_DIR: .data/${worktree}
`)
	requireNamedService(t, cfg, Service{
		Name: "api", Prepare: "build --data ${worktree}", Cmd: "run --data ${worktree}",
		PortAuto: true, Env: map[string]string{"DATA_DIR": ".data/${worktree}"},
	})
	if !cfg.UsesAssignedPorts() || cfg.UsesManifestExtras() {
		t.Errorf("UsesAssignedPorts=%v UsesManifestExtras=%v, want true, false", cfg.UsesAssignedPorts(), cfg.UsesManifestExtras())
	}
	requireMarshalDeclares(t, cfg, ""+
		"name: demo\n"+
		"services:\n"+
		"  - name: api\n"+
		"    prepare: build --data ${worktree}\n"+
		"    cmd: run --data ${worktree}\n"+
		"    port: auto\n"+
		"    env:\n"+
		"      DATA_DIR: .data/${worktree}\n")
}

func TestLoadReferenceProblems(t *testing.T) {
	for _, tt := range []struct{ name, body, problem string }{
		{"a port that is neither", "services:\n  - name: api\n    port: fast\n",
			`line 4: port must be a number or auto, not "fast"`},
		{"an unknown service", "services:\n  - name: api\n    port: auto\n    cmd: run ${nope.port}\n",
			`service api: cmd ${nope.port} names no service "nope"`},
		{"an unknown service in prepare", "services:\n  - name: api\n    port: auto\n    prepare: build --port ${nope.port}\n",
			`service api: prepare ${nope.port} names no service "nope"`},
		{"a service with no port", "services:\n  - name: db\n  - name: api\n    env:\n      DB: ${db.port}\n",
			"service api: env DB ${db.port} needs a port, and db declares none"},
		{"its own port when it has none", "services:\n  - name: api\n    cmd: run --port ${port}\n",
			"service api: cmd ${port} needs a port, and api declares none"},
		{"a bad variable name", "services:\n  - name: api\n    env:\n      BAD-KEY: x\n",
			`service api: env "BAD-KEY" is not a variable name`},
		{"a workspace reference in health is refused", "workspace:\n  - name: backend\n    worktree: ../backend\n    service: api\nservices:\n  - name: api\n    health:\n      command: curl -f ${backend.url}/health\n",
			"service api: health ${backend.url} names a workspace reference; workspace addresses are expanded in prepare, cmd and env only"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requireConfigProblems(t, refuseLoad(t, "demo", "name: demo\n"+tt.body), tt.problem)
		})
	}
}

func TestLoadAcceptsMachineAndWorkspaceReferences(t *testing.T) {
	cfg := mustLoad(t, "demo", `name: demo
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
	requireNamedService(t, cfg, Service{
		Name: "api", Prepare: "build --api ${backend.url}",
		Cmd:    "run --db ${mysql.port} --backend ${backend.url}",
		Health: "command:mysqladmin -h 127.0.0.1 -P ${mysql.port} ping",
		Env:    map[string]string{"API_URL": "${backend.url}"},
	})
	wantMachine := []MachineRef{{Name: "mysql", Port: 3306}}
	wantWorkspace := []WorkspaceRef{{Name: "backend", Worktree: "../backend", Service: "api"}}
	if !reflect.DeepEqual(cfg.Machine, wantMachine) {
		t.Errorf("machine = %#v, want %#v", cfg.Machine, wantMachine)
	}
	if !reflect.DeepEqual(cfg.Workspace, wantWorkspace) {
		t.Errorf("workspace = %#v, want %#v", cfg.Workspace, wantWorkspace)
	}
	if !cfg.UsesAssignedPorts() || !cfg.UsesManifestExtras() {
		t.Errorf("UsesAssignedPorts=%v UsesManifestExtras=%v, want true, true", cfg.UsesAssignedPorts(), cfg.UsesManifestExtras())
	}
	// Marshal writes services first, then machine, then workspace — not the
	// input order — and keeps the references unexpanded.
	requireMarshalDeclares(t, cfg, ""+
		"name: demo\n"+
		"services:\n"+
		"  - name: api\n"+
		"    prepare: build --api ${backend.url}\n"+
		"    cmd: run --db ${mysql.port} --backend ${backend.url}\n"+
		"    health:\n"+
		"      command: mysqladmin -h 127.0.0.1 -P ${mysql.port} ping\n"+
		"    env:\n"+
		"      API_URL: ${backend.url}\n"+
		"machine:\n"+
		"  - name: mysql\n"+
		"    port: 3306\n"+
		"workspace:\n"+
		"  - name: backend\n"+
		"    worktree: ../backend\n"+
		"    service: api\n")
}

func TestWorkspaceValidationProblems(t *testing.T) {
	for _, tt := range []struct{ name, body, problem string }{
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
			requireConfigProblems(t, refuseLoad(t, "demo", "name: demo\n"+tt.body), tt.problem)
		})
	}
}

func requireWorktreeToken(t *testing.T, root, got string) {
	t.Helper()
	sum := sha256.Sum256([]byte(Canonical(root)))
	want := "wt_" + hex.EncodeToString(sum[:])[:12]
	if got != want {
		t.Errorf("WorktreeToken(%q) = %q, want %q (wt_ + 12 hex of sha256(Canonical))", root, got, want)
	}
}

func requireNamedService(t *testing.T, cfg *Config, want Service) {
	t.Helper()
	got, ok := cfg.ServiceNamed(want.Name)
	if !ok {
		t.Fatalf("no service named %q", want.Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("service %s = %#v, want %#v", want.Name, got, want)
	}
	if got.HasPort() != (want.Port != 0 || want.PortAuto) {
		t.Errorf("service %s HasPort() = %v, want %v", want.Name, got.HasPort(), want.Port != 0 || want.PortAuto)
	}
}

func requireMarshalDeclares(t *testing.T, cfg *Config, body string) {
	t.Helper()
	out, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(out)
	if !strings.Contains(text, body) {
		t.Errorf("Marshal is missing the declared document:\n%s\n--- want body ---\n%s", text, body)
	}
	back, err := Parse(cfg.Path, out)
	if err != nil {
		t.Fatalf("the marshalled file does not load: %v\n%s", err, out)
	}
	if back.Name != cfg.Name {
		t.Errorf("round-trip Name = %q, want %q", back.Name, cfg.Name)
	}
	for _, svc := range cfg.Services {
		requireNamedService(t, back, svc)
	}
	if !reflect.DeepEqual(back.Machine, cfg.Machine) {
		t.Errorf("round-trip machine = %#v, want %#v", back.Machine, cfg.Machine)
	}
	if !reflect.DeepEqual(back.Workspace, cfg.Workspace) {
		t.Errorf("round-trip workspace = %#v, want %#v", back.Workspace, cfg.Workspace)
	}
}
