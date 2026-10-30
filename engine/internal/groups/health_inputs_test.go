package groups

import (
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestHealthFormsParseAndCanonicalize(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"path", "health: /healthz\n", "/healthz"},
		{"http mapping", "health:\n      http: /live\n", "/live"},
		{"tcp", "health:\n      tcp: 5432\n", "tcp:5432"},
		{"command", "health:\n      command: pg_isready -h 127.0.0.1 -p ${port}\n", "command:pg_isready -h 127.0.0.1 -p ${port}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadString(t, "shop", "name: shop\nservices:\n  - name: db\n    port: 5432\n    cmd: serve\n    "+tc.body)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Services[0].Health; got != tc.want {
				t.Fatalf("health = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHealthMappingRoundTrips(t *testing.T) {
	svc := Service{Name: "db", Port: 5432, Cmd: "serve", Health: "tcp:5432"}
	out, err := yaml.Marshal(svc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(out), "tcp: 5432") {
		t.Fatalf("tcp health did not render as a mapping:\n%s", out)
	}
	var back Service
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.Health != svc.Health {
		t.Fatalf("health = %q, want %q", back.Health, svc.Health)
	}

	svc = Service{Name: "db", Cmd: "serve", Health: "command:pg_isready -p 5432"}
	out, err = yaml.Marshal(svc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(out), "command: pg_isready -p 5432") {
		t.Fatalf("command health did not render as a mapping:\n%s", out)
	}
}

func TestHealthValidationRefusesAmbiguousAndUnanchoredChecks(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"reserved scalar", "health: tcp:5432", "reserved prefix"},
		{"missing slash", "health: healthz", "must start with /"},
		{"tcp without port", "health:\n      tcp: 5432", "declare a port"},
		{"tcp mismatch", "port: 5433\n    health:\n      tcp: 5432", "does not match"},
		{"tcp auto", "port: auto\n    health:\n      tcp: 5432", "fixed port"},
		{"two forms", "health:\n      http: /x\n      tcp: 5432", "exactly one"},
		{"empty mapping", "health: {}", "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := "\nname: shop\nservices:\n  - name: db\n    cmd: serve\n    " + tc.body + "\n"
			_, err := loadString(t, "shop", body)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestPrepareInputsParseAndValidate(t *testing.T) {
	cfg, err := loadString(t, "web", `
name: web
services:
  - name: web
    prepare: npm ci
    prepare_inputs: [package.json, package-lock.json]
    cmd: npm start
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Services[0].PrepareInputs; len(got) != 2 || got[0] != "package.json" || got[1] != "package-lock.json" {
		t.Fatalf("prepare_inputs = %v", got)
	}
	if got := cfg.Services[0].Prepare; got != "npm ci" {
		t.Fatalf("prepare = %q", got)
	}

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"no command", "prepare_inputs: [package.json]", "needs a prepare command"},
		{"absolute", "prepare: npm ci\n    prepare_inputs: [/etc/passwd]", "must be relative"},
		{"escape", "prepare: npm ci\n    prepare_inputs: [../../secret]", "escapes the directory"},
		{"empty", "prepare: npm ci\n    prepare_inputs: ['']", "empty path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadString(t, "web", "\nname: web\nservices:\n  - name: web\n    cmd: npm start\n    "+tc.body+"\n")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestPrepareInputsStayInsideProject(t *testing.T) {
	cfg, err := loadString(t, "web", `
name: web
services:
  - name: web
    cwd: app
    prepare: npm ci
    prepare_inputs: [../package.json]
    cmd: npm start
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(cfg.Dir, "package.json")
	if got := filepath.Join(cfg.Dir, filepath.FromSlash("app"), filepath.FromSlash("../package.json")); got != want {
		t.Fatalf("path resolution = %q, want %q", got, want)
	}
}

func TestServiceNodeRendersPrepareInputsAndHealthMapping(t *testing.T) {
	out, err := yaml.Marshal(serviceNode(ServiceAdd{
		Name: "web", Prepare: "npm ci", PrepareInputs: []string{"package.json"},
		Port: 5432, Health: "tcp:5432",
	}))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, want := range []string{"prepare: npm ci", "prepare_inputs: [package.json]", "tcp: 5432"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("rendered service is missing %q:\n%s", want, out)
		}
	}
}

func TestHealthCommandPortReferenceCountsAsAssignedPort(t *testing.T) {
	cfg := &Config{Services: []Service{{
		Name: "db", Port: 5432, Health: "command:pg_isready -p ${port}",
	}}}
	if !cfg.UsesAssignedPorts() {
		t.Fatal("a ${port} reference in health did not require assigned ports")
	}
}
