package groups

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// sampleManifest is the file the round-trip tests start from: comments above,
// beside and inside the services list, and a key order no one would choose
// alphabetically.
const sampleManifest = `# option-berth's own checkout.
name: option-berth
ports: [9229]
services:
  # prime the database first.
  - name: db
    cmd: docker compose up db
    port: 5433
  - name: api
    cmd: uv run api          # uvicorn
    cwd: backend
    port: 8001
    health: /health
    depends_on: [db]
`

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ConfigName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// patchOne applies a single-service patch and fails the test on error.
func patchOne(t *testing.T, path, name string, patch ServicePatch) *Config {
	t.Helper()
	cfg, err := PatchServices(path, []ServiceEdit{{Name: name, Patch: patch}})
	if err != nil {
		t.Fatalf("PatchServices(%s): %v", name, err)
	}
	return cfg
}

func patchError(t *testing.T, path, name string, patch ServicePatch) error {
	t.Helper()
	_, err := PatchServices(path, []ServiceEdit{{Name: name, Patch: patch}})
	return err
}

func requireMentions(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
}

func serviceLines(t *testing.T, yaml, name string) []string {
	t.Helper()
	var lines []string
	capturing := false
	for _, line := range strings.Split(yaml, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") {
			capturing = trimmed == "- name: "+name
			if capturing {
				lines = append(lines, strings.TrimPrefix(trimmed, "- "))
			}
			continue
		}
		if capturing {
			if !strings.HasPrefix(line, "    ") || trimmed == "" {
				break
			}
			lines = append(lines, trimmed)
		}
	}
	if len(lines) == 0 {
		t.Fatalf("no service %q in:\n%s", name, yaml)
	}
	return lines
}

func keyNames(lines []string) []string {
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if name, _, ok := strings.Cut(line, ":"); ok {
			names = append(names, strings.TrimSpace(name))
		}
	}
	return names
}

func sameStrings(a, b []string) bool {
	return slices.Equal(a, b)
}

// equalStrings keeps the older test files on the same comparison.
func equalStrings(a, b []string) bool { return sameStrings(a, b) }

// An edit made from a client must not eat the comments the author wrote.
func TestPatchKeepsComments(t *testing.T) {
	path := writeManifest(t, sampleManifest)

	cfg := patchOne(t, path, "db", ServicePatch{}.
		SetString(FieldHealth, "/").
		SetString(FieldColor, "blue"))

	out := readFile(t, path)
	requireMentions(t, out,
		"# option-berth's own checkout.",
		"# prime the database first.",
		"# uvicorn",
		"health: /",
		"color: blue")

	if got := keyNames(serviceLines(t, out, "db")); !sameStrings(got, []string{"name", "cmd", "port", "health", "color"}) {
		t.Errorf("db keys = %v", got)
	}
	if cfg.Services[0].Health != "/" || cfg.Services[0].Color != "blue" {
		t.Errorf("the returned config lacks the patch: %+v", cfg.Services[0])
	}
	if cfg.Services[1].Cmd != "uv run api" || len(cfg.Ports) != 1 || cfg.Ports[0] != 9229 {
		t.Errorf("unrelated keys changed: %+v", cfg)
	}
}

func TestPatchNullClearsTheKey(t *testing.T) {
	path := writeManifest(t, sampleManifest)

	patchOne(t, path, "api", ServicePatch{}.Clear(FieldHealth))

	out := readFile(t, path)
	if strings.Contains(out, "health:") {
		t.Errorf("health was not cleared:\n%s", out)
	}
	requireMentions(t, out, "cwd: backend", "depends_on:")
}

func TestPatchFromJSONDistinguishesAbsentFromNull(t *testing.T) {
	var patch ServicePatch
	if err := json.Unmarshal([]byte(`{"description": "The API", "health": null}`), &patch); err != nil {
		t.Fatal(err)
	}
	if !sameStrings(patch.Sent, []string{"description", "health"}) {
		t.Fatalf("sent keys = %v", patch.Sent)
	}

	path := writeManifest(t, sampleManifest)
	cfg := patchOne(t, path, "api", patch)
	api := cfg.Services[1]
	if api.Description != "The API" {
		t.Errorf("description not written: %+v", api)
	}
	if api.Health != "" {
		t.Errorf("null did not clear health: %+v", api)
	}
	if api.Cwd != "backend" || api.Port != 8001 {
		t.Errorf("untouched keys were rewritten: %+v", api)
	}
}

func TestPatchUnknownServiceLeavesTheFileAlone(t *testing.T) {
	path := writeManifest(t, sampleManifest)
	before := readFile(t, path)

	err := patchError(t, path, "worker", ServicePatch{}.SetString(FieldIcon, "gear"))
	var missing *ServiceNotFoundError
	if !errors.As(err, &missing) || missing.Name != "worker" {
		t.Fatalf("err = %v, want ServiceNotFoundError for worker", err)
	}
	if readFile(t, path) != before {
		t.Error("the file changed despite the error")
	}
}

func TestPatchInvalidResultIsNotWritten(t *testing.T) {
	path := writeManifest(t, sampleManifest)
	before := readFile(t, path)

	err := patchError(t, path, "api", ServicePatch{}.SetPort(70000))
	var bad *ConfigError
	if !errors.As(err, &bad) {
		t.Fatalf("err = %v, want *ConfigError", err)
	}
	if len(bad.Problems) == 0 || !strings.Contains(bad.Error(), "70000") {
		t.Errorf("the error should name the bad port: %v", bad)
	}
	if readFile(t, path) != before {
		t.Error("an invalid patch reached the disk")
	}
}

// A colour like "#3b82f6" is a comment in plain YAML, so the writer must quote
// it and the file must read back.
func TestPatchQuotesWhenItMust(t *testing.T) {
	path := writeManifest(t, sampleManifest)

	cfg := patchOne(t, path, "db", ServicePatch{}.
		SetString(FieldColor, "#3b82f6").
		SetString(FieldDescription, "Postgres: the one with the data"))
	if cfg.Services[0].Color != "#3b82f6" {
		t.Fatalf("colour = %q", cfg.Services[0].Color)
	}

	again, err := Load(path)
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if again.Services[0].Color != "#3b82f6" ||
		again.Services[0].Description != "Postgres: the one with the data" {
		t.Fatalf("re-read lost a value: %+v", again.Services[0])
	}
}

func TestPatchRepeatedEditsDoNotDuplicateKeys(t *testing.T) {
	path := writeManifest(t, sampleManifest)

	for _, icon := range []string{"database", "server"} {
		patchOne(t, path, "db", ServicePatch{}.SetString(FieldIcon, icon))
	}
	out := readFile(t, path)
	if count := strings.Count(out, "icon:"); count != 1 {
		t.Fatalf("icon written %d times:\n%s", count, out)
	}
	if !strings.Contains(out, "icon: server") {
		t.Fatalf("the second edit did not win:\n%s", out)
	}
}

// A patch that changes nothing still has to name a service that exists.
func TestPatchChecksTheNameEvenWhenEmpty(t *testing.T) {
	path := writeManifest(t, sampleManifest)
	err := patchError(t, path, "worker", ServicePatch{})
	var missing *ServiceNotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v, want ServiceNotFoundError", err)
	}
}

// Trailing comment padding is a documented yaml.v3 limitation: the comment
// survives, but its column does not, so `cmd: x     # note` comes back as
// `cmd: x # note`. This test notices if a future version starts preserving it.
func TestPatchCollapsesTrailingCommentPadding(t *testing.T) {
	path := writeManifest(t, sampleManifest)

	patchOne(t, path, "db", ServicePatch{}.SetString(FieldColor, "blue"))

	out := readFile(t, path)
	if !strings.Contains(out, "cmd: uv run api # uvicorn") {
		t.Errorf("the trailing comment did not survive with one space:\n%s", out)
	}
	if strings.Contains(out, "cmd: uv run api          # uvicorn") {
		t.Errorf("yaml.v3 now keeps the padding; drop the caveat:\n%s", out)
	}
}
