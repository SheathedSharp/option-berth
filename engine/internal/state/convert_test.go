package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

func TestFromListeningCarriesTheContractFields(t *testing.T) {
	scan := ports.ListeningPort{
		Port: 5173, BindAddress: "127.0.0.1", IPVersion: "IPv4",
		PID: 48211, PPID: 48190, Process: "node",
		Command:     "node /Users/me/code/option-berth/frontend/node_modules/.bin/vite",
		Cwd:         "/Users/me/code/option-berth/frontend",
		ProjectRoot: "/Users/me/code/option-berth", Group: "option-berth", GroupSource: "auto",
		User: "me", Type: ports.PortTypeUser,
		Tag: "frontend", RunID: "a1b2c3d4", RunRootPID: 48190,
		StartedAt: "2026-09-02T09:12:41+02:00",
	}
	row := FromListening(scan)

	if row.PPID != 48190 || row.URL != "http://127.0.0.1:5173" {
		t.Fatalf("row = %+v", row)
	}
	if row.DisplayName == "" {
		t.Fatal("display_name was not computed")
	}
	if row.ProjectRoot == nil || *row.ProjectRoot != "/Users/me/code/option-berth" {
		t.Fatalf("project_root = %v", row.ProjectRoot)
	}
	if row.Group == nil || *row.Group != "option-berth" {
		t.Fatalf("group = %v", row.Group)
	}
	if row.GroupSource == nil || *row.GroupSource != SourceAuto {
		t.Fatalf("group_source = %v", row.GroupSource)
	}
	if row.Run == nil || row.Run.ID != "a1b2c3d4" ||
		row.Run.Name != "frontend" || row.Run.RootPID != 48190 {
		t.Fatalf("run = %+v", row.Run)
	}
	if row.StartedAt == nil || *row.StartedAt != "2026-09-02T09:12:41+02:00" {
		t.Fatalf("started_at = %v", row.StartedAt)
	}
	if row.Type != TypeUser {
		t.Fatalf("type = %q", row.Type)
	}
	if row.Stats != nil || row.Health != nil || row.Docker != nil {
		t.Fatal("uncollected sub-objects are not nil")
	}
	if row.Name != nil {
		t.Fatalf("name = %v, want null", row.Name)
	}
}

func TestFromListeningRecognisesATrashedWorkingDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory to build paths from")
	}
	cases := []struct {
		label string
		cwd   string
		want  bool
	}{
		{"a deleted project", filepath.Join(home, ".Trash", "bookkeeping-acceptance", "redis"), true},
		{"a live project", filepath.Join(home, "code", "shop"), false},
		{"no working directory", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			row := FromListening(ports.ListeningPort{Port: 16379, Process: "redis-server", Cwd: tc.cwd})
			if row.CwdInTrash != tc.want {
				t.Errorf("cwd_in_trash for %q = %v, want %v", tc.cwd, row.CwdInTrash, tc.want)
			}
		})
	}
}

func TestFromListeningKeepsTheGoneDirectoryMarker(t *testing.T) {
	// The process outlived its deleted working directory and still listens.
	row := FromListening(ports.ListeningPort{
		Port: 5173, Process: "node",
		Cwd:     "/Users/me/code/tmp/uat_for_example-worker/frontend/example-project-web",
		CwdGone: true,
	})
	if !row.CwdGone {
		t.Error("cwd_gone = false")
	}
	if row.CwdInTrash {
		t.Error("a deleted directory was labelled as trashed")
	}
	if !ToListening(row).CwdGone {
		t.Error("cwd_gone did not survive the round trip")
	}
}

func TestCollectedEnrichmentsBecomeObjects(t *testing.T) {
	row := FromListening(ports.ListeningPort{
		Port: 5432, BindAddress: "0.0.0.0", Type: ports.PortTypeDocker,
		CPUPercent: 1.2, MemoryRSS: 183500800, ThreadCount: 14,
		Uptime: "2h13m", State: "sleeping", Connections: 2,

		// Probe and container.
		HealthStatus: "ok", HealthCode: 200, HealthLatency: 4 * time.Millisecond,
		DockerContainer: "db", DockerImage: "postgres:16",
		DockerComposeService: "db", DockerComposeProject: "option-berth", DockerContainerPort: 5432,
	})
	if row.Stats == nil || row.Stats.CPUPercent != 1.2 || row.Stats.Connections != 2 {
		t.Fatalf("stats = %+v", row.Stats)
	}
	if row.Health == nil || row.Health.Code != 200 || row.Health.LatencyMs != 4 {
		t.Fatalf("health = %+v", row.Health)
	}
	if row.Docker == nil || row.Docker.ComposeService != "db" {
		t.Fatalf("docker = %+v", row.Docker)
	}
	if row.Type != TypeDocker {
		t.Fatalf("type = %q", row.Type)
	}
}

func TestUncollectedStatsMarshalAsNull(t *testing.T) {
	encoded, err := json.Marshal(FromListening(ports.ListeningPort{Port: 3000, BindAddress: "::1"}))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["stats"] != nil {
		t.Fatalf("stats = %v, want null", fields["stats"])
	}
	for _, required := range []string{"display_name", "ppid"} {
		if _, present := fields[required]; !present {
			t.Fatalf("%q is missing from the row", required)
		}
	}
	if _, dead := fields["exposed_urls"]; dead {
		t.Fatal("the removed exposed_urls key still marshals")
	}
}
