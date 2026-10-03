package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/runsreg"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/runs"
	"github.com/sheathedsharp/option-berth/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/internal/state"
	"github.com/sheathedsharp/option-berth/internal/store"
)

func TestDirectRunRegistryReportsNoPortWorker(t *testing.T) {
	// This fixture registers the test runner itself. Keep it out of the
	// package registry: later real-process tests must not inherit its run tree.
	t.Setenv("BERTH_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, groups.ConfigName), []byte(`name: shop
services:
  - name: worker
    cmd: python3 worker.py
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runs.Add(runs.Entry{
		PID: os.Getpid(), Group: "shop", Name: "worker", Cmd: "python3 worker.py",
	}); err != nil {
		t.Fatalf("runs.Add: %v", err)
	}

	index := groups.NewIndex()
	index.Observe(root)
	got := groups.GroupsWith(nil, index, directRunRegistry{reg: runs.Load()})
	if len(got) != 1 || len(got[0].Services) != 1 {
		t.Fatalf("groups = %+v, want one project with one service", got)
	}
	worker := got[0].Services[0]
	if !worker.Running {
		t.Fatalf("worker = %+v, want running from runs.json without a port", worker)
	}
	if worker.PortActual != nil {
		t.Fatalf("worker port_actual = %v, want null", worker.PortActual)
	}
	if worker.PID == nil || *worker.PID != os.Getpid() {
		t.Fatalf("worker pid = %v, want the live run pid", worker.PID)
	}
	if worker.StartedAt == nil || *worker.StartedAt == "" {
		t.Fatalf("worker started_at = %v, want the live run timestamp", worker.StartedAt)
	}
}

func TestDirectRunRegistryCarriesRuntimeDriftEvidence(t *testing.T) {
	run := runs.Entry{
		PID: os.Getpid(), Group: "shop", Name: "api", ID: "run-api",
		Cmd: "new-api", Cwd: "/nowhere/shop", SpecHash: "runtime-hash",
		StartedAt: "2026-09-30T10:00:00Z",
	}
	got := directRunRegistry{reg: &runs.Registry{Runs: map[int]runs.Entry{run.PID: run}}}
	service := got.ServiceFacts()[servicefacts.Key{Group: "shop", Service: "api"}].Run
	if service == nil || service.Cmd != run.Cmd || service.Cwd != run.Cwd || service.SpecHash != run.SpecHash {
		t.Fatalf("ServiceFacts run = %+v; want command, cwd and spec hash", service)
	}
}

func TestDirectAndDaemonServiceFactsProduceTheSameRows(t *testing.T) {
	index := groups.NewIndex()
	for _, name := range []string{"left", "right"} {
		index.Add(&groups.Config{Name: name, Dir: "/nonexistent/" + name, Services: []groups.Service{
			{Name: "api", Cmd: "serve", PortAuto: true}, {Name: "worker", Cmd: "work"},
		}})
	}
	daemon := runsreg.New()
	daemon.Mirror = false
	now := time.Unix(10, 0)
	direct := directRunRegistry{reg: &runs.Registry{Runs: map[int]runs.Entry{}}}
	for i, group := range []string{"left", "right"} {
		record := runsreg.Record{ID: group + "-api", PID: i + 1, Group: group, Name: "api", Cmd: "serve", PortHint: 8100 + i, StartedAt: now}
		daemon.Register(record)
		direct.reg.Runs[record.PID] = runs.Entry{
			ID: record.ID, PID: record.PID, Group: group, Name: record.Name, Cmd: record.Cmd,
			PortHint: record.PortHint, StartedAt: now.UTC().Format(time.RFC3339),
		}
	}
	worker := runsreg.Record{ID: "left-worker", PID: 3, Group: "left", Name: "worker", StartedAt: now}
	daemon.Register(worker)
	direct.reg.Runs[worker.PID] = runs.Entry{ID: worker.ID, PID: worker.PID, Group: worker.Group, Name: worker.Name, StartedAt: now.UTC().Format(time.RFC3339)}
	for i, name := range []string{"worker", "api"} {
		record := runsreg.Record{ID: "old-" + name, PID: 10 + i, Group: "right", Name: name, StartedAt: now}
		daemon.Register(record)
		exit, _ := daemon.Exited(record.PID, 1, false)
		direct.exits = append([]store.RunExitRow{{
			ID: record.ID, Group: record.Group, Name: name, Code: exit.Code, Reason: exit.Reason, ExitedAt: exit.ExitedAt,
		}}, direct.exits...)
	}
	left, right := "left", "right"
	listeners := []state.Port{{Group: &left, Port: 8100, PID: 100}, {Group: &right, Port: 8101, PID: 101}}
	want := groups.GroupsWith(listeners, index, daemon)
	got := groups.GroupsWith(listeners, index, direct)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("direct=%+v\ndaemon=%+v", got, want)
	}
	if got[1].Services[0].LastExit != nil || !got[1].Services[0].Running {
		t.Fatal("old api exit overrode the current run")
	}
	if got[0].Services[1].PortActual != nil || !got[0].Services[1].Running {
		t.Fatal("portless worker lost its independent run evidence")
	}
}
