package state

import "testing"

func TestDiffReportsAddUpdateRemoveAndRestart(t *testing.T) {
	before := Snapshot{Seq: 1, Ports: []Port{
		{Port: 3000, BindAddress: "127.0.0.1", PID: 10, DisplayName: "a"},
		{Port: 5432, BindAddress: "0.0.0.0", PID: 20, DisplayName: "db"},
	}}
	after := Snapshot{Seq: 2, Ports: []Port{
		{Port: 3000, BindAddress: "127.0.0.1", PID: 11, DisplayName: "a"}, // new pid = restart
		{Port: 5432, BindAddress: "0.0.0.0", PID: 20, DisplayName: "database"},
		{Port: 8080, BindAddress: "0.0.0.0", PID: 30, DisplayName: "new"},
	}}

	delta := Diff(before, after)
	if len(delta.Ports.Added) != 2 {
		t.Fatalf("added = %d, want the new port and the restarted one", len(delta.Ports.Added))
	}
	if len(delta.Ports.Removed) != 1 || delta.Ports.Removed[0] != "3000:127.0.0.1" {
		t.Fatalf("removed = %v", delta.Ports.Removed)
	}
	if len(delta.Ports.Updated) != 1 || delta.Ports.Updated[0].DisplayName != "database" {
		t.Fatalf("updated = %+v", delta.Ports.Updated)
	}
	if delta.Seq != 2 {
		t.Fatalf("seq = %d", delta.Seq)
	}
}

func TestStatsChangesNeedTheOptIn(t *testing.T) {
	before := Snapshot{Ports: []Port{{Port: 1, BindAddress: "x", PID: 1, Stats: &Stats{CPUPercent: 1}}}}
	after := Snapshot{Ports: []Port{{Port: 1, BindAddress: "x", PID: 1, Stats: &Stats{CPUPercent: 2}}}}

	if delta := Diff(before, after); len(delta.Ports.Updated) != 0 {
		t.Fatalf("a stats-only change produced an update: %+v", delta.Ports.Updated)
	}
	if delta := DiffWithStats(before, after); len(delta.Ports.Updated) != 1 {
		t.Fatal("DiffWithStats did not report the stats change")
	}
}

func TestGroupsDiffByStatusAndMembers(t *testing.T) {
	before := Snapshot{Groups: []Group{{Name: "a", Status: "running", Members: []int{1}}}}
	after := Snapshot{Groups: []Group{
		{Name: "a", Status: "partial", Members: []int{1}},
		{Name: "b", Status: "stopped"},
	}}

	delta := Diff(before, after)
	if len(delta.Groups.Added) != 1 || delta.Groups.Added[0].Name != "b" {
		t.Fatalf("added = %+v", delta.Groups.Added)
	}
	if len(delta.Groups.Updated) != 1 || delta.Groups.Updated[0].Status != "partial" {
		t.Fatalf("updated = %+v", delta.Groups.Updated)
	}
	if len(delta.Groups.Removed) != 0 {
		t.Fatalf("removed = %v", delta.Groups.Removed)
	}
}

func TestGroupIdentityAndMachineChangesArePublished(t *testing.T) {
	root := "/code/example-worker"
	config := "/code/example-worker/oberth.yaml"
	before := Group{
		Host: "localhost", Name: "example-worker", Repo: "example-worker",
		RootDir: &root, ConfigPath: &config, Source: SourceFile,
		Machine: []MachineRef{{Name: "db", Port: 5432, Listening: false}},
	}
	after := before
	after.Source = SourceStart
	after.Machine = []MachineRef{{Name: "db", Port: 5432, Listening: true}}

	delta := Diff(Snapshot{Groups: []Group{before}}, Snapshot{Groups: []Group{after}})
	if len(delta.Groups.Updated) != 1 ||
		delta.Groups.Updated[0].Source != SourceStart ||
		!delta.Groups.Updated[0].Machine[0].Listening {
		t.Fatalf("group delta = %+v, want one updated group", delta.Groups)
	}
}

// A probe that merely answers faster must not publish a delta: configured
// health runs every tick, so latency in the comparison would mean a delta on
// every tick forever.
func TestHealthLatencyAloneIsNotADelta(t *testing.T) {
	base := Port{Port: 3000, BindAddress: "127.0.0.1", PID: 10}
	before := base
	before.Health = &Health{Status: HealthOK, Code: 200, LatencyMs: 3}
	after := base
	after.Health = &Health{Status: HealthOK, Code: 200, LatencyMs: 40}

	delta := Diff(Snapshot{Ports: []Port{before}}, Snapshot{Ports: []Port{after}})
	if len(delta.Ports.Updated) != 0 {
		t.Fatalf("latency-only change published an update: %+v", delta.Ports.Updated)
	}

	after.Health = &Health{Status: HealthFail, Code: 500, LatencyMs: 40}
	delta = Diff(Snapshot{Ports: []Port{before}}, Snapshot{Ports: []Port{after}})
	if len(delta.Ports.Updated) != 1 {
		t.Fatalf("a status change did not publish: %+v", delta.Ports)
	}
	if delta.Ports.Updated[0].Health.LatencyMs != 40 {
		t.Fatalf("the update must carry the newest latency: %+v", delta.Ports.Updated[0].Health)
	}
}

func TestHealthObservationTimeAloneIsNotADelta(t *testing.T) {
	groupAt := func(observed string) Group {
		return Group{Name: "demo", Services: []Service{{
			Name: "api", HealthStatus: &Health{Status: HealthOK, Code: 200, ObservedAt: observed},
		}}}
	}
	before := groupAt("2026-09-24T09:00:00Z")
	after := groupAt("2026-09-24T09:00:10Z")
	if delta := Diff(Snapshot{Groups: []Group{before}}, Snapshot{Groups: []Group{after}}); len(delta.Groups.Updated) != 0 {
		t.Fatalf("observation-time-only change published %+v", delta.Groups.Updated)
	}
}

func TestServiceHealthLatencyAloneIsNotADelta(t *testing.T) {
	groupWithLatency := func(latency int64) Group {
		return Group{Name: "demo", Services: []Service{{
			Name: "api", HealthStatus: &Health{Status: HealthOK, Code: 200, LatencyMs: latency},
		}}}
	}
	before := groupWithLatency(3)
	after := groupWithLatency(40)
	if delta := Diff(Snapshot{Groups: []Group{before}}, Snapshot{Groups: []Group{after}}); len(delta.Groups.Updated) != 0 {
		t.Fatalf("service latency-only change published %+v", delta.Groups.Updated)
	}
}
