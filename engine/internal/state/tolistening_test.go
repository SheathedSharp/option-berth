package state

import (
	"reflect"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// completeScanRow fills every field the published shape carries, so a field
// added on one side only makes the round-trip test fail loudly.
func completeScanRow() ports.ListeningPort {
	return ports.ListeningPort{
		// The socket.
		Port: 8000,
		PID:  42,
		PPID: 7,

		// What the process is.
		Process:   "python3",
		Command:   "python3 -m uvicorn app:app",
		ParentCmd: "npm run dev",

		// Where and how it listens.
		Cwd:         "/home/dev/api",
		User:        "dev",
		BindAddress: "127.0.0.1",
		IPVersion:   "IPv4",
		Type:        ports.PortTypeUser,

		// The run and its manifest placement.
		Tag:        "web",
		RunID:      "run-1",
		RunGroup:   "shop",
		RunRootPID: 40,

		// The declared service.
		Name:        "api",
		ProjectRoot: "/home/dev/api",
		Group:       "api",
		GroupSource: "file",

		// Start-time evidence.
		StartedAt: "2026-09-05T10:00:00Z",

		// Resource sample.
		CPUPercent:  12.5,
		MemoryRSS:   1024,
		ThreadCount: 8,

		// Current process state.
		Uptime:      "3h",
		State:       "sleeping",
		Connections: 3,

		// Health probe.
		HealthStatus:  "healthy",
		HealthCode:    200,
		HealthLatency: 12 * time.Millisecond,

		// Container behind the port.
		DockerContainer: "api-1",
		DockerImage:     "api:latest",

		// Compose placement.
		DockerComposeService: "api",
		DockerComposeProject: "shop",
		DockerContainerPort:  8000,
	}
}

func TestPublishedRowSurvivesAScanRoundTrip(t *testing.T) {
	published := FromListening(completeScanRow())
	back := FromListening(ToListening(published))
	if !reflect.DeepEqual(published, back) {
		t.Fatalf("round trip changed the published row:\n before %+v\n after  %+v", published, back)
	}
}

// DisplayName is the one derived value the wire cannot recompute: it needs the
// parent cmdline, which is not published, so the converter carries the
// resolved name across.
func TestRoundTripKeepsTheResolvedDisplayName(t *testing.T) {
	site := "/home/dev/site"
	row := ports.ListeningPort{
		Port:      3000,
		Process:   "node",
		Command:   "node " + site + "/node_modules/.bin/next dev",
		ParentCmd: "npm run dev",
		Cwd:       site,
	}

	returned := ToListening(FromListening(row))
	if returned.ParentCmd != "" {
		t.Fatal("the unpublished parent cmdline reappeared")
	}
	if got := returned.DisplayName(); got != row.DisplayName() {
		t.Fatalf("display name after the round trip = %q, want %q", got, row.DisplayName())
	}
}

func TestPortTypesMapBothWays(t *testing.T) {
	pairs := []struct {
		scan ports.PortType
		wire PortType
	}{
		{ports.PortTypeUser, TypeUser},
		{ports.PortTypeDocker, TypeDocker},
	}
	for _, pair := range pairs {
		published := FromListening(ports.ListeningPort{Port: 1, Type: pair.scan})
		if published.Type != pair.wire {
			t.Fatalf("FromListening(%v).Type = %q, want %q", pair.scan, published.Type, pair.wire)
		}
		if got := ToListening(published).Type; got != pair.scan {
			t.Fatalf("ToListening(%q).Type = %v, want %v", pair.wire, got, pair.scan)
		}
	}
}

func TestAbsentEnrichmentsStayNull(t *testing.T) {
	published := FromListening(ports.ListeningPort{Port: 5432, Process: "postgres"})
	if published.Stats != nil || published.Health != nil ||
		published.Docker != nil || published.Run != nil {
		t.Fatalf("a bare row published enrichments: %+v", published)
	}
	back := ToListening(published)
	if back.CPUPercent != 0 || back.HealthStatus != "" ||
		back.DockerContainer != "" || back.RunID != "" {
		t.Fatalf("null enrichments converted back to values: %+v", back)
	}
}
