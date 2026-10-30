package groups

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// The manifest schema is checked in like the protocol schema: a Go type change
// that is not reflected in the published file fails here, not in an editor
// somewhere after release.
func TestCheckedInManifestSchemaIsUpToDate(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "schema", "manifest.schema.json")
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go generate ./...`)", err)
	}
	if strings.ReplaceAll(string(onDisk), "\r\n", "\n") != string(MarshalManifestSchema()) {
		t.Fatalf("%s is stale; run `go generate ./...`", path)
	}
}

func TestManifestSchemaAcceptsACompleteManifest(t *testing.T) {
	schema := compileManifestSchema(t)
	if err := validateManifestYAML(t, schema, `# editor modeline is a comment and does not change the document
$schema: https://example.invalid/manifest.schema.json
name: demo
services:
  - name: api
    prepare: make build
    prepare_inputs: [package.json, package-lock.json]
    cmd: ./bin/api
    cwd: backend
    port: auto
    health: /healthz
    description: HTTP API
    icon: api
    color: blue
    depends_on: [db]
    env:
      API_URL: http://127.0.0.1:${port}
  - name: db
    cmd: docker compose up db
    port: 5432
    health:
      tcp: 5432
  - name: cache
    cmd: redis-server
    port: 6379
    health:
      command: redis-cli -p ${port} ping
machine:
  - name: mysql
    port: 3306
ports: [9229]
worktree_ports: 4
x-template: &defaults
  health: /ready
`); err != nil {
		t.Fatalf("a valid manifest was refused: %v", err)
	}
}

// The file option-berth writes must be a file its own published schema
// accepts; otherwise `oberth init` would produce a warning before the user
// typed anything.
func TestWrittenManifestValidates(t *testing.T) {
	schema := compileManifestSchema(t)
	cfg := &Config{
		Name: "demo",
		Services: []Service{
			{Name: "api", Cmd: "echo api", PortAuto: true, Env: map[string]string{"API_URL": "http://127.0.0.1:${port}"}},
			{Name: "web", Prepare: "npm ci", PrepareInputs: []string{"package.json"}, Cmd: "npm start"},
			{Name: "db", Cmd: "postgres", Port: 5432, Health: "tcp:5432"},
			{Name: "cache", Cmd: "redis-server", Port: 6379, Health: "command:redis-cli -p ${port} ping"},
		},
		Machine: []MachineRef{{Name: "mysql", Port: 3306}},
	}
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateManifestYAML(t, schema, string(data)); err != nil {
		t.Fatalf("generated manifest was refused by its own schema: %v\n%s", err, data)
	}
}

// The whole point of publishing the schema is that an editor marks a typo
// before the file is adopted. Every shape below is one the loader either
// accepts with a warning or refuses at runtime.
func TestManifestSchemaRejectsMisspellingsAndBadValues(t *testing.T) {
	schema := compileManifestSchema(t)
	for _, tc := range []struct {
		name string
		body string
	}{
		{"top-level typo", "name: demo\nservcies: []\n"},
		{"service typo", "services:\n  - name: api\n    heath: /healthz\n"},
		{"machine typo", "machine:\n  - name: db\n    port: 5432\n    prott: 5433\n"},
		{"port is not a string", "services:\n  - name: api\n    port: \"8000\"\n"},
		{"port out of range", "services:\n  - name: api\n    port: 70000\n"},
		{"health path has no slash", "services:\n  - name: api\n    health: healthz\n"},
		{"health has two forms", "services:\n  - name: db\n    port: 5432\n    health:\n      tcp: 5432\n      command: true\n"},
		{"health tcp is not a number", "services:\n  - name: db\n    port: 5432\n    health:\n      tcp: \"5432\"\n"},
		{"health tcp out of range", "services:\n  - name: db\n    port: 5432\n    health:\n      tcp: 70000\n"},
		{"health command is empty", "services:\n  - name: api\n    health:\n      command: \"\"\n"},
		{"health has an unknown form", "services:\n  - name: api\n    health:\n      httpz: /health\n"},
		{"prepare_inputs is a string", "services:\n  - name: api\n    prepare: make build\n    prepare_inputs: package.json\n"},
		{"machine port required", "machine:\n  - name: db\n"},
		{"service name required", "services:\n  - cmd: echo api\n"},
		{"top-level extension is not a service", "name: demo\nservices: {name: api}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateManifestYAML(t, schema, tc.body); err == nil {
				t.Fatalf("schema accepted:\n%s", tc.body)
			}
		})
	}
}

func TestStrictManifestRejectsUnknownFields(t *testing.T) {
	t.Setenv(StrictEnv, "1")
	path := filepath.Join(t.TempDir(), ConfigName)
	body := "name: demo\nservices:\n  - name: api\n    cmd: echo api\n    heath: /healthz\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	var configErr *ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("Load error = %v, want a *ConfigError", err)
	}
	text := err.Error()
	if !strings.Contains(text, "services[0].heath") {
		t.Errorf("error does not name the misspelled key: %v", err)
	}
	if !strings.Contains(text, "line 5:5") {
		t.Errorf("error does not locate the key: %v", err)
	}
	if strings.Contains(text, "(ignored)") {
		t.Errorf("strict mode still calls the field ignored: %v", err)
	}
}

// Metadata keys are part of the format: `$schema` names the editor schema and
// top-level `x-` keys carry reusable anchors. Strict mode must not turn either
// into an error.
func TestStrictManifestAcceptsMetadataKeys(t *testing.T) {
	t.Setenv(StrictEnv, "1")
	path := filepath.Join(t.TempDir(), ConfigName)
	body := `name: demo
$schema: https://example.invalid/manifest.schema.json
x-template: &defaults
  cmd: echo api
services:
  - <<: *defaults
    name: api
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("strict mode refused metadata: %v", err)
	}
}

// Strict mode must accept the new key shapes: prepare_inputs is a known key,
// and the keys inside a health mapping belong to the health union, not to the
// service.
func TestStrictManifestAcceptsHealthAndPrepareInputs(t *testing.T) {
	t.Setenv(StrictEnv, "1")
	path := filepath.Join(t.TempDir(), ConfigName)
	body := `name: demo
services:
  - name: db
    prepare: npm ci
    prepare_inputs: [package.json]
    cmd: postgres
    port: 5432
    health:
      tcp: 5432
  - name: api
    cmd: api
    health:
      command: pg_isready -p 5432
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("strict mode refused a health mapping or prepare_inputs: %v", err)
	}
}

func TestUnknownFieldsStayWarningsByDefault(t *testing.T) {
	t.Setenv(StrictEnv, "")
	path := filepath.Join(t.TempDir(), ConfigName)
	if err := os.WriteFile(path, []byte("name: demo\nservcies: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, warnings, err := LoadWithWarnings(path)
	if err != nil {
		t.Fatalf("default parsing refused an unknown field: %v", err)
	}
	if len(warnings) != 1 || warnings[0].Field != "servcies" {
		t.Fatalf("warnings = %+v", warnings)
	}
}

func TestStrictManifestSwitch(t *testing.T) {
	for _, value := range []string{"1", "true", "yes", "on", "TRUE", " on "} {
		t.Setenv(StrictEnv, value)
		if !StrictManifest() {
			t.Errorf("%q did not enable strict mode", value)
		}
	}
	for _, value := range []string{"", "0", "false", "no", "off"} {
		t.Setenv(StrictEnv, value)
		if StrictManifest() {
			t.Errorf("%q enabled strict mode", value)
		}
	}
}

func compileManifestSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(MarshalManifestSchema()))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("manifest.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	schema, err := c.Compile("manifest.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func validateManifestYAML(t *testing.T, schema *jsonschema.Schema, body string) error {
	t.Helper()
	var value any
	if err := yaml.Unmarshal([]byte(body), &value); err != nil {
		t.Fatalf("invalid fixture YAML: %v", err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("fixture does not convert to JSON: %v", err)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	return schema.Validate(document)
}
