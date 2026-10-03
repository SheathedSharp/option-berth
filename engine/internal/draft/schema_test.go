package draft

import (
	"testing"

	"github.com/sheathedsharp/option-berth/internal/groups"
)

// ptr builds the nullable fields the schema requires every answer to carry.
func ptr[T any](v T) *T { return &v }

// The renderer is the only thing that writes a draft file now, so its shape is
// frozen here: field order, where the provenance comments land, and that the
// result is a manifest the engine's own loader accepts.
func TestDraftRendersAManifest(t *testing.T) {
	d := &Draft{
		Name: "proj",
		Services: []DraftService{{
			Name:     "api",
			Prepare:  ptr("mvn -DskipTests package"),
			Cmd:      "pnpm run dev",
			Cwd:      ptr("backend"),
			Port:     ptr(8000),
			Health:   ptr("/healthz"),
			Env:      []DraftEnv{{Name: "NODE_ENV", Value: "development"}, {Name: "API_URL", Value: "http://127.0.0.1:${port}"}},
			Why:      "package.json scripts.dev",
			Verified: ptr("already listening on 8000 when inspected"),
		}},
		Machine: []DraftMachine{{Name: "mysql", Port: 3306}},
		Notes:   []string{"the worker has no port"},
	}

	out, err := d.YAML()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rendered:\n%s", out)

	cfg, err := groups.Parse("draft.yaml", out)
	if err != nil {
		t.Fatalf("the engine's own loader refuses what the renderer wrote: %v\n%s", err, out)
	}
	if cfg.Name != "proj" || len(cfg.Services) != 1 {
		t.Fatalf("parsed back as %q with %d services", cfg.Name, len(cfg.Services))
	}
	if cfg.Services[0].Env["API_URL"] != "http://127.0.0.1:${port}" {
		t.Errorf("env did not survive the round trip: %+v", cfg.Services[0].Env)
	}
	if cfg.Services[0].Prepare != "mvn -DskipTests package" {
		t.Errorf("prepare did not survive the round trip: %q", cfg.Services[0].Prepare)
	}
	if len(cfg.Machine) != 1 || cfg.Machine[0].Name != "mysql" || cfg.Machine[0].Port != 3306 {
		t.Errorf("the machine list did not survive the round trip: %+v\n%s", cfg.Machine, out)
	}
}

// An empty answer is a valid answer, and it has to stay a manifest.
func TestDraftRendersAnEmptyServiceList(t *testing.T) {
	out, err := (&Draft{Name: "proj"}).YAML()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rendered:\n%s", out)
	if _, err := groups.Parse("draft.yaml", out); err != nil {
		t.Fatalf("empty draft does not parse: %v\n%s", err, out)
	}
}
