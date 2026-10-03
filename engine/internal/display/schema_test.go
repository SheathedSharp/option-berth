package display

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// TestPortRowsValidateAgainstPortSchema is the acceptance gate for the rows
// `oberth status --json` prints in `ports[]`: they must satisfy the checked-in
// Port definition.
func TestPortRowsValidateAgainstPortSchema(t *testing.T) {
	sch := compilePortSchema(t)

	rows := []ports.ListeningPort{
		// A bare row: nothing enriched, every optional null.
		{Port: 3000, BindAddress: "127.0.0.1", PID: 42, Process: "node"},
		// A fully enriched native row started by `option-berth run`.
		{
			Port: 5173, BindAddress: "::1", IPVersion: "IPv6", PID: 48211, PPID: 48190,
			Process: "node", Command: "node vite", Cwd: "/tmp/proj/frontend",
			ProjectRoot: "/tmp/proj", Group: "proj", GroupSource: "auto",
			User: "me", Type: ports.PortTypeUser,
			Tag: "frontend", RunID: "a1b2", RunRootPID: 48190,
			StartedAt:  "2026-09-02T09:12:41+02:00",
			CPUPercent: 1.2, MemoryRSS: 1835008, ThreadCount: 14,
			Uptime: "2h13m", State: "sleeping", Connections: 2,
			HealthStatus: "ok", HealthCode: 200, HealthLatency: 4 * time.Millisecond,
		},
		// A Docker row.
		{
			Port: 5432, BindAddress: "0.0.0.0", IPVersion: "IPv4", PID: 9,
			Process: "com.docke", Type: ports.PortTypeDocker,
			DockerContainer: "proj-db-1", DockerImage: "postgres:16",
			DockerComposeService: "db", DockerComposeProject: "proj",
			DockerContainerPort: 5432,
		},
	}

	decoded := encodePorts(t, rows)
	if len(decoded) != len(rows) {
		t.Fatalf("got %d rows, want %d", len(decoded), len(rows))
	}
	for i, row := range decoded {
		if err := sch.Validate(any(row)); err != nil {
			t.Errorf("row %d does not validate against #/definitions/Port:\n%v", i, err)
		}
	}
}

// encodePorts turns scan rows into the wire rows `status` prints and decodes
// them back as generic JSON — the schema gates below are about that shape.
func encodePorts(t *testing.T, rows []ports.ListeningPort) []map[string]any {
	t.Helper()
	wire := make([]state.Port, 0, len(rows))
	for _, r := range rows {
		wire = append(wire, state.FromListening(r))
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func compilePortSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "schema", "protocol.schema.json")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%v (run `go generate ./...`)", err)
	}
	defer f.Close()

	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("protocol.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("protocol.schema.json#/definitions/Port")
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

// TestPortRowsCarryResolvedGroups: with no daemon, the resolver's group,
// group_source and project_root reach the wire rows, and the rows still
// satisfy the checked-in Port definition.
func TestPortRowsCarryResolvedGroups(t *testing.T) {
	repo := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(repo); err == nil {
		repo = resolved
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "name: fixture\nservices:\n  - name: api\n    port: 8000\n"
	if err := os.WriteFile(filepath.Join(repo, ".oberth.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	rows := []ports.ListeningPort{
		{Port: 8000, BindAddress: "127.0.0.1", PID: 42, Process: "python3", Cwd: repo},
		{Port: 3000, BindAddress: "127.0.0.1", PID: 43, Process: "node"},
	}
	groups.Attribute(rows)

	if rows[0].Group != "fixture" || rows[0].GroupSource != "file" || rows[0].ProjectRoot != repo {
		t.Fatalf("resolved row = {group: %q, source: %q, root: %q}",
			rows[0].Group, rows[0].GroupSource, rows[0].ProjectRoot)
	}
	if rows[1].Group != "" {
		t.Errorf("port with no cwd got group %q, want none", rows[1].Group)
	}

	decoded := encodePorts(t, rows)
	if decoded[0]["group"] != "fixture" || decoded[0]["group_source"] != "file" {
		t.Errorf("json row = %v", decoded[0])
	}
	if decoded[1]["group"] != nil || decoded[1]["group_source"] != nil {
		t.Errorf("ungrouped json row = %v", decoded[1])
	}

	sch := compilePortSchema(t)
	for i, row := range decoded {
		if err := sch.Validate(any(row)); err != nil {
			t.Errorf("row %d does not validate against #/definitions/Port:\n%v", i, err)
		}
	}
}
