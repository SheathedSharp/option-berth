package state

import "testing"

func TestDiffAddedUpdatedRemovedAndRestart(t *testing.T) {
	prev := Snapshot{Seq: 1, Ports: []Port{
		{Port: 3000, BindAddress: "127.0.0.1", PID: 10, DisplayName: "a"},
		{Port: 5432, BindAddress: "0.0.0.0", PID: 20, DisplayName: "db"},
	}}
	next := Snapshot{Seq: 2, Ports: []Port{
		{Port: 3000, BindAddress: "127.0.0.1", PID: 11, DisplayName: "a"}, // pid changed = restart
		{Port: 5432, BindAddress: "0.0.0.0", PID: 20, DisplayName: "database"},
		{Port: 8080, BindAddress: "0.0.0.0", PID: 30, DisplayName: "new"},
	}}
	d := Diff(prev, next)
	if len(d.Ports.Added) != 2 { // 8080 and restarted 3000
		t.Fatalf("added = %d, want 2", len(d.Ports.Added))
	}
	if len(d.Ports.Removed) != 1 || d.Ports.Removed[0] != "3000:127.0.0.1" {
		t.Fatalf("removed = %v, want [3000:127.0.0.1]", d.Ports.Removed)
	}
	if len(d.Ports.Updated) != 1 || d.Ports.Updated[0].DisplayName != "database" {
		t.Fatalf("updated = %+v", d.Ports.Updated)
	}
	if d.Seq != 2 {
		t.Fatalf("seq = %d", d.Seq)
	}
}

func TestDiffStatsOnlyChangeIsNotAnUpdateWithoutStats(t *testing.T) {
	prev := Snapshot{Ports: []Port{{Port: 1, BindAddress: "x", PID: 1, Stats: &Stats{CPUPercent: 1}}}}
	next := Snapshot{Ports: []Port{{Port: 1, BindAddress: "x", PID: 1, Stats: &Stats{CPUPercent: 2}}}}
	if d := Diff(prev, next); len(d.Ports.Updated) != 0 {
		t.Fatalf("stats-only change must not produce Updated, got %+v", d.Ports.Updated)
	}
	if d := DiffWithStats(prev, next); len(d.Ports.Updated) != 1 {
		t.Fatalf("DiffWithStats must report stats change")
	}
}

func TestDiffGroupsComparedByStatusAndMembers(t *testing.T) {
	prev := Snapshot{Groups: []Group{{Name: "a", Status: "running", Members: []int{1}}}}
	next := Snapshot{Groups: []Group{
		{Name: "a", Status: "partial", Members: []int{1}},
		{Name: "b", Status: "stopped"},
	}}
	d := Diff(prev, next)
	if len(d.Groups.Added) != 1 || d.Groups.Added[0].Name != "b" {
		t.Fatalf("added = %+v", d.Groups.Added)
	}
	if len(d.Groups.Updated) != 1 || d.Groups.Updated[0].Status != "partial" {
		t.Fatalf("updated = %+v", d.Groups.Updated)
	}
	if len(d.Groups.Removed) != 0 {
		t.Fatalf("removed = %v", d.Groups.Removed)
	}
}

func TestDiffGroupsPublishesIdentityAndMachineMetadataChanges(t *testing.T) {
	root := "/code/example-worker"
	config := "/code/example-worker/oberth.yaml"
	prevGroup := Group{
		Host: "localhost", Name: "example-worker", Repo: "example-worker", RootDir: &root,
		ConfigPath: &config, Source: SourceFile,
		Machine: []MachineRef{{Name: "db", Port: 5432, Listening: false}},
	}
	nextGroup := prevGroup
	nextGroup.Source = SourceStart
	nextGroup.Machine = []MachineRef{{Name: "db", Port: 5432, Listening: true}}
	d := Diff(Snapshot{Groups: []Group{prevGroup}}, Snapshot{Groups: []Group{nextGroup}})
	if len(d.Groups.Updated) != 1 || d.Groups.Updated[0].Source != SourceStart || !d.Groups.Updated[0].Machine[0].Listening {
		t.Fatalf("group metadata delta = %+v, want one updated group", d.Groups)
	}
}

// TestDiffIgnoresHealthLatency keeps a health probe that answers a millisecond
// faster from publishing a delta: with configured health polled every tick,
// latency in the comparison would mean a delta on every tick forever.
func TestDiffIgnoresHealthLatency(t *testing.T) {
	base := Port{Port: 3000, BindAddress: "127.0.0.1", PID: 10}
	prev := base
	prev.Health = &Health{Status: HealthOK, Code: 200, LatencyMs: 3}
	next := base
	next.Health = &Health{Status: HealthOK, Code: 200, LatencyMs: 40}

	d := Diff(Snapshot{Ports: []Port{prev}}, Snapshot{Ports: []Port{next}})
	if len(d.Ports.Updated) != 0 {
		t.Fatalf("latency-only change published an update: %+v", d.Ports.Updated)
	}

	next.Health = &Health{Status: HealthFail, Code: 500, LatencyMs: 40}
	d = Diff(Snapshot{Ports: []Port{prev}}, Snapshot{Ports: []Port{next}})
	if len(d.Ports.Updated) != 1 {
		t.Fatalf("a status change must publish an update, got %+v", d.Ports)
	}
	if d.Ports.Updated[0].Health.LatencyMs != 40 {
		t.Fatalf("the published row must carry the newest latency, got %+v", d.Ports.Updated[0].Health)
	}
}

func TestDiffIgnoresHealthObservationTime(t *testing.T) {
	service := func(at string) Service {
		return Service{Name: "api", HealthStatus: &Health{Status: HealthOK, Code: 200, ObservedAt: at}}
	}
	prev := Group{Name: "demo", Services: []Service{service("2026-09-24T09:00:00Z")}}
	next := Group{Name: "demo", Services: []Service{service("2026-09-24T09:00:10Z")}}
	if d := Diff(Snapshot{Groups: []Group{prev}}, Snapshot{Groups: []Group{next}}); len(d.Groups.Updated) != 0 {
		t.Fatalf("observation-time-only change published a group update: %+v", d.Groups.Updated)
	}
}

func TestDiffIgnoresServiceHealthLatency(t *testing.T) {
	service := func(latency int64) Service {
		return Service{Name: "api", HealthStatus: &Health{Status: HealthOK, Code: 200, LatencyMs: latency}}
	}
	prev := Group{Name: "demo", Services: []Service{service(3)}}
	next := Group{Name: "demo", Services: []Service{service(40)}}
	if d := Diff(Snapshot{Groups: []Group{prev}}, Snapshot{Groups: []Group{next}}); len(d.Groups.Updated) != 0 {
		t.Fatalf("service latency-only change published an update: %+v", d.Groups.Updated)
	}
}
