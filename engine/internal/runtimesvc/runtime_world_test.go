package runtimesvc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimecore"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

func worldTestGroup(t *testing.T) (state.Group, string) {
	t.Helper()
	root := t.TempDir()
	config := filepath.Join(root, "oberth.yaml")
	if err := os.WriteFile(config, []byte("name: shop\nservices: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return state.Group{
		Name:       "shop",
		Identity:   "wt_0123456789ab",
		Branch:     "main",
		RootDir:    &root,
		ConfigPath: &config,
	}, root
}

func TestServerWorldProjectionAndObserver(t *testing.T) {
	group, root := worldTestGroup(t)
	port, pid := 3000, 4242
	runID := "run-1"
	group.Services = []state.Service{{
		Name: "api", Running: true, RunID: &runID, PID: &pid,
		Port: &port, PortActual: &port,
	}}
	snapshot := state.Snapshot{
		Seq: 3, At: "2026-07-01T12:00:00Z",
		Ports: []state.Port{{
			Host: "localhost", Port: port, PID: pid,
			Run: &state.Run{ID: runID, Group: "shop", Name: "api"},
		}},
		Groups:      []state.Group{group},
		Observation: &state.Observation{State: state.ObservationObservable},
	}
	server := New(Options{Socket: "/test/world-observer.sock", Version: "test"})
	var observed []runtimecore.World
	server.RegisterWorldObserver(func(world runtimecore.World) {
		observed = append(observed, world)
	})

	server.publish(state.Snapshot{}, snapshot, nil)

	if len(observed) != 1 {
		t.Fatalf("observer calls = %d, want 1", len(observed))
	}
	if observed[0].Health != runtimecore.WorldHealthy {
		t.Fatalf("observed health = %q, want healthy", observed[0].Health)
	}
	byGroup, ok := server.World("shop", "")
	if !ok || byGroup.Worktree.Group != "shop" || byGroup.Worktree.Root != root {
		t.Fatalf("world by group = %+v, ok=%v", byGroup, ok)
	}
	byRoot, ok := server.World("", root)
	if !ok || byRoot.Revision != byGroup.Revision {
		t.Fatalf("world by root = %+v, ok=%v", byRoot, ok)
	}
}

func TestServerWorldFeedsTheLoopDetector(t *testing.T) {
	group, _ := worldTestGroup(t)
	server := New(Options{Socket: "/test/world-loops.sock", Version: "test"})
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	for i, runID := range []string{"run-1", "run-2", "run-3"} {
		at := base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		group.Services = []state.Service{{
			Name: "api",
			LastExit: &state.ServiceExit{
				Code: 1, Reason: "crashed", RunID: runID, At: at,
			},
		}}
		server.publish(state.Snapshot{}, state.Snapshot{
			Seq: uint64(i + 1), At: at, Groups: []state.Group{group},
			Observation: &state.Observation{State: state.ObservationObservable},
		}, nil)
	}

	world, ok := server.World("shop", "")
	if !ok {
		t.Fatal("no world after three crashed runs")
	}
	if len(world.Diagnosis.Loops) != 1 || world.Diagnosis.Loops[0].Kind != runtimecore.LoopCrashLoop {
		t.Fatalf("loops = %+v, want one crash_loop", world.Diagnosis.Loops)
	}
	if world.Health != runtimecore.WorldBroken {
		t.Fatalf("health = %q, want broken", world.Health)
	}
	if len(world.Diagnosis.RootCauses) != 1 || world.Diagnosis.RootCauses[0].Kind != runtimecore.DriftRepeatedCrash {
		t.Fatalf("root causes = %+v, want one repeated_crash", world.Diagnosis.RootCauses)
	}
}

func TestServerRunExitNotificationFeedsTheLoopDetector(t *testing.T) {
	group, _ := worldTestGroup(t)
	server := New(Options{Socket: "/test/world-run-exit.sock", Version: "test"})
	t.Cleanup(server.detachRunExit)
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	for i, runID := range []string{"run-1", "run-2", "run-3"} {
		at := base.Add(time.Duration(i) * time.Minute)
		group.Services = []state.Service{{Name: "api", Running: true, RunID: &runID}}
		server.publish(state.Snapshot{}, state.Snapshot{
			Seq: uint64(i + 1), At: at.Format(time.RFC3339), Groups: []state.Group{group},
			Observation: &state.Observation{State: state.ObservationObservable},
		}, nil)
		NotifyRunExit(RunExit{RunID: runID, Code: 1, Reason: "crashed", ExitedAt: at})
	}
	// One more transition publishes the world after the last exit was fed.
	server.publish(state.Snapshot{}, state.Snapshot{
		Seq: 4, At: base.Add(3 * time.Minute).Format(time.RFC3339), Groups: []state.Group{group},
		Observation: &state.Observation{State: state.ObservationObservable},
	}, nil)

	world, ok := server.World("shop", "")
	if !ok {
		t.Fatal("no world after three exit notifications")
	}
	var crash *runtimecore.LoopSignal
	for i := range world.Diagnosis.Loops {
		if world.Diagnosis.Loops[i].Kind == runtimecore.LoopCrashLoop {
			crash = &world.Diagnosis.Loops[i]
		}
	}
	if crash == nil || crash.Count != 3 {
		t.Fatalf("loops = %+v, want a crash_loop of 3 from the exit hook", world.Diagnosis.Loops)
	}
}

func TestRuntimeWorldReadHandlerReadsBothSelectors(t *testing.T) {
	group, root := worldTestGroup(t)
	group.Services = []state.Service{{Name: "worker", Running: true}}
	server := New(Options{Socket: "/test/world-read.sock", Version: "test"})
	server.publish(state.Snapshot{}, state.Snapshot{
		Seq: 1, At: "2026-07-01T12:00:00Z", Groups: []state.Group{group},
		Observation: &state.Observation{State: state.ObservationObservable},
	}, nil)

	byGroup := &Request{HostContext: server.host, Params: json.RawMessage(`{"group":"shop"}`)}
	result, err := handleRuntimeWorldRead(context.Background(), byGroup)
	if err != nil {
		t.Fatalf("world read by group: %v", err)
	}
	if result.(rpc.RuntimeWorldReadResult).World.Worktree.Group != "shop" {
		t.Fatalf("result = %+v", result)
	}

	byPath := &Request{HostContext: server.host, Params: json.RawMessage(`{"worktree_path":` + quoteJSON(root) + `}`)}
	if _, err := handleRuntimeWorldRead(context.Background(), byPath); err != nil {
		t.Fatalf("world read by path: %v", err)
	}

	missing := &Request{HostContext: server.host, Params: json.RawMessage(`{"group":"nope"}`)}
	_, err = handleRuntimeWorldRead(context.Background(), missing)
	var protocolError *rpc.Error
	if !errors.As(err, &protocolError) || protocolError.Code != rpc.CodeNotFound {
		t.Fatalf("missing world error = %v, want not_found", err)
	}
}

func TestServerWorldPopulatesFirstErrorFromStoreRunExits(t *testing.T) {
	group, root := worldTestGroup(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	logPath := filepath.Join(t.TempDir(), "auth.log")
	prev := "ERROR: previous generation\n"
	curr := "starting auth\nERROR: invalid JWT_SECRET\n"
	if err := os.WriteFile(logPath, []byte(prev+curr), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	if err := db.AppendRunExit(store.RunExitRow{
		ID:        "auth-run-2",
		PID:       4242,
		Group:     "shop",
		Name:      "auth",
		Cwd:       root,
		LogPath:   logPath,
		LogOffset: int64(len(prev)),
		Code:      1,
		Reason:    "crashed",
		StartedAt: now.Add(-time.Second),
		ExitedAt:  now,
		LastLines: []string{"ERROR: invalid JWT_SECRET"},
	}); err != nil {
		t.Fatalf("AppendRunExit: %v", err)
	}

	group.Services = []state.Service{
		{
			Name:    "auth",
			LogPath: &logPath,
			LastExit: &state.ServiceExit{
				Code: 1, Reason: "crashed", RunID: "auth-run-2", At: now.Format(time.RFC3339),
			},
		},
		{
			Name:      "api",
			DependsOn: []string{"auth"},
			LastExit: &state.ServiceExit{
				Code: 1, Reason: "dependency_not_ready", RunID: "api-run-2", At: now.Format(time.RFC3339),
			},
		},
	}

	server := New(Options{Socket: "/test/world-first-error.sock", Version: "test"})
	server.host.Store = db
	var observed []runtimecore.World
	server.RegisterWorldObserver(func(w runtimecore.World) { observed = append(observed, w) })

	server.publish(state.Snapshot{}, state.Snapshot{
		Seq: 1, At: now.Format(time.RFC3339), Groups: []state.Group{group},
		Observation: &state.Observation{State: state.ObservationObservable},
	}, nil)

	if len(observed) != 1 || len(observed[0].Diagnosis.RootCauses) != 1 {
		t.Fatalf("observed = %+v, want one root cause", observed)
	}
	rootCause := observed[0].Diagnosis.RootCauses[0]
	if rootCause.Service != "auth" || rootCause.FirstError != "ERROR: invalid JWT_SECRET" {
		t.Fatalf("root cause = %+v, want auth first error from run offset", rootCause)
	}

	// Once the service recovers, the cached failed run id is released.
	runID := "auth-run-3"
	group.Services = []state.Service{{Name: "auth", Running: true, RunID: &runID}}
	server.publish(state.Snapshot{}, state.Snapshot{
		Seq: 2, At: now.Add(time.Minute).Format(time.RFC3339), Groups: []state.Group{group},
		Observation: &state.Observation{State: state.ObservationObservable},
	}, nil)
	server.worldMu.RLock()
	cached := len(server.exitErrors)
	server.worldMu.RUnlock()
	if cached != 0 {
		t.Fatalf("exitErrors cache kept %d stale entries after recovery", cached)
	}
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
