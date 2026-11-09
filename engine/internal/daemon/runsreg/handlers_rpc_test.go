package runsreg

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// resetRegistry gives one test its own registry so the package-level Default
// cannot carry a run between tests. It is restored on cleanup.
func resetRegistry(t *testing.T) {
	t.Helper()
	previous := Default
	Default = New()
	t.Cleanup(func() { Default = previous })
}

// handlerRuntime builds the runtime the handlers act through: a logger and a
// loop whose collector returns nothing, so a handler that invalidates or reads
// the snapshot makes no OS call.
func handlerRuntime(t *testing.T) *daemon.Runtime {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	loop := scanner.New(scanner.Options{
		DaemonVersion: "test",
		Logger:        logger,
		Scan:          func(scanner.Include) ([]ports.ListeningPort, error) { return nil, nil },
	})
	return &daemon.Runtime{Version: "test", Logger: logger, Scanner: loop}
}

// handlerRequest frames one call the way the dispatcher does: params under the
// method the handler sees.
func handlerRequest(t *testing.T, rt *daemon.Runtime, method string, params any) *daemon.Request {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshalling %s params: %v", method, err)
	}
	return &daemon.Request{Method: method, ID: json.RawMessage("1"), Params: raw, Runtime: rt}
}

// TestHandleRegisterRecordsRunAndSession pins the whole registration path: the
// reply carries the id the registry assigned, the run lands under its pid, and
// a session the caller sent is written through to the store.
func TestHandleRegisterRecordsRunAndSession(t *testing.T) {
	resetRegistry(t)
	rt := handlerRuntime(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "register.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	rt.Store = st

	session := &state.Session{ID: "sess-1", Tool: "claude-code", Label: "demo"}
	result, err := handleRegister(context.Background(), handlerRequest(t, rt, "runs.register", rpc.RunsRegisterParams{
		// The registry prunes runs whose process is gone, so the test registers
		// the pid that is certainly alive: the test binary's own.
		PID: os.Getpid(), Group: "demo", Name: "api", Cwd: t.TempDir(), AllowOutsideHome: true,
		Session: session,
	}))
	if err != nil {
		t.Fatalf("runs.register: %v", err)
	}
	registered, ok := result.(rpc.RunsRegisterResult)
	if !ok || registered.ID == "" {
		t.Fatalf("result = %#v, want a RunsRegisterResult with an id", result)
	}
	runs := Default.List()
	if len(runs) != 1 || runs[0].PID != os.Getpid() || runs[0].ID != registered.ID {
		t.Fatalf("registry runs = %+v, want the registered process", runs)
	}
	row, found, err := st.Sessions().Get("sess-1")
	if err != nil || !found || row.Tool != "claude-code" || row.Label != "demo" {
		t.Fatalf("stored session = %+v found=%v err=%v, want the caller's session", row, found, err)
	}
}

// TestHandleRegisterRefusesAPidlessRun pins the one field a registration
// cannot omit: without a pid there is no process to attribute a port to.
func TestHandleRegisterRefusesAPidlessRun(t *testing.T) {
	resetRegistry(t)
	rt := handlerRuntime(t)
	_, err := handleRegister(context.Background(), handlerRequest(t, rt, "runs.register", rpc.RunsRegisterParams{
		Cwd: t.TempDir(), AllowOutsideHome: true,
	}))
	var protocolError *rpc.Error
	if err == nil || !asRPCError(err, &protocolError) || protocolError.Code != rpc.CodeInvalidParams {
		t.Fatalf("runs.register without a pid = %v, want invalid_params", err)
	}
}

// TestHandleUnregisterCarriesTheOutcome pins the two ways a run leaves the
// registry: with an exit code it becomes exit history, without one it is only
// forgotten.
func TestHandleUnregisterCarriesTheOutcome(t *testing.T) {
	t.Run("exit code becomes history", func(t *testing.T) {
		resetRegistry(t)
		rt := handlerRuntime(t)
		Default.Register(Record{PID: 5151, Group: "demo", Name: "api"})
		code := 0
		result, err := handleUnregister(context.Background(), handlerRequest(t, rt, "runs.unregister", rpc.RunsUnregisterParams{
			PID: 5151, ExitCode: &code,
		}))
		if err != nil {
			t.Fatalf("runs.unregister: %v", err)
		}
		if _, ok := result.(rpc.OKResult); !ok {
			t.Fatalf("result = %#v, want OK", result)
		}
		if len(Default.List()) != 0 || len(Default.Exits()) != 1 {
			t.Fatalf("after an exit: runs=%d exits=%d, want 0/1", len(Default.List()), len(Default.Exits()))
		}
	})
	t.Run("no exit code only forgets", func(t *testing.T) {
		resetRegistry(t)
		rt := handlerRuntime(t)
		Default.Register(Record{PID: 6161, Group: "demo", Name: "worker"})
		if _, err := handleUnregister(context.Background(), handlerRequest(t, rt, "runs.unregister", rpc.RunsUnregisterParams{PID: 6161})); err != nil {
			t.Fatalf("runs.unregister: %v", err)
		}
		if len(Default.List()) != 0 || len(Default.Exits()) != 0 {
			t.Fatalf("after a plain unregister: runs=%d exits=%d, want 0/0", len(Default.List()), len(Default.Exits()))
		}
	})
}

// TestHandleUnregisterRefusesAPidlessCall keeps the same pid rule on the
// unregister side.
func TestHandleUnregisterRefusesAPidlessCall(t *testing.T) {
	resetRegistry(t)
	rt := handlerRuntime(t)
	_, err := handleUnregister(context.Background(), handlerRequest(t, rt, "runs.unregister", rpc.RunsUnregisterParams{}))
	var protocolError *rpc.Error
	if err == nil || !asRPCError(err, &protocolError) || protocolError.Code != rpc.CodeInvalidParams {
		t.Fatalf("runs.unregister without a pid = %v, want invalid_params", err)
	}
}

// TestHandleListReturnsLiveRunsAndExits pins that `runs.list` reports both the
// live registry and the exit history, and that it derives nothing about a run's
// ports from a collector that is not answering.
func TestHandleListReturnsLiveRunsAndExits(t *testing.T) {
	resetRegistry(t)
	rt := handlerRuntime(t)
	Default.Register(Record{PID: 7171, Group: "demo", Name: "api"})
	Default.Exited(7171, 1, false)

	result, err := handleList(context.Background(), handlerRequest(t, rt, "runs.list", struct{}{}))
	if err != nil {
		t.Fatalf("runs.list: %v", err)
	}
	list, ok := result.(rpc.RunsListResult)
	if !ok {
		t.Fatalf("result = %#v, want RunsListResult", result)
	}
	if len(list.Runs) != 0 {
		t.Errorf("runs = %+v, want none: the process exited", list.Runs)
	}
	if len(list.Exited) != 1 || list.Exited[0].PID != 7171 {
		t.Fatalf("exited = %+v, want the one recorded exit", list.Exited)
	}
}

// TestHandleListPropagatesCancellation: a cancelled read is not an empty list.
func TestHandleListPropagatesCancellation(t *testing.T) {
	resetRegistry(t)
	rt := handlerRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := handleList(ctx, handlerRequest(t, rt, "runs.list", struct{}{})); err == nil {
		t.Fatal("runs.list on a cancelled context returned no error")
	}
}

// TestHandleSpawnRejectsAnEmptyArgv stops before spawning: a run with no
// command is the caller's mistake, reported without touching the process table.
func TestHandleSpawnRejectsAnEmptyArgv(t *testing.T) {
	resetRegistry(t)
	rt := handlerRuntime(t)
	_, err := handleSpawn(context.Background(), handlerRequest(t, rt, "runs.spawn", rpc.RunsSpawnParams{
		Cwd: t.TempDir(), AllowOutsideHome: true,
	}))
	var protocolError *rpc.Error
	if err == nil || !asRPCError(err, &protocolError) || protocolError.Code != rpc.CodeInvalidParams {
		t.Fatalf("runs.spawn without argv = %v, want invalid_params", err)
	}
}

// TestHandleSpawnRefusesOutsideHome stops at the home boundary: the same rule
// that guards runs.register applies before any process is started.
func TestHandleSpawnRefusesOutsideHome(t *testing.T) {
	resetRegistry(t)
	rt := handlerRuntime(t)
	_, err := handleSpawn(context.Background(), handlerRequest(t, rt, "runs.spawn", rpc.RunsSpawnParams{
		Argv: []string{"/bin/true"}, Cwd: string(filepath.Separator),
	}))
	var protocolError *rpc.Error
	if err == nil || !asRPCError(err, &protocolError) || protocolError.Code != rpc.CodeOutsideHome {
		t.Fatalf("runs.spawn outside home = %v, want outside_home", err)
	}
}

// TestHandleSpawnReportsASpawnFailure: a command the OS will not start is an
// internal error the caller can act on, not a silent ok with a missing run.
func TestHandleSpawnReportsASpawnFailure(t *testing.T) {
	resetRegistry(t)
	rt := handlerRuntime(t)
	_, err := handleSpawn(context.Background(), handlerRequest(t, rt, "runs.spawn", rpc.RunsSpawnParams{
		Argv: []string{filepath.Join(t.TempDir(), "does-not-exist")},
		Cwd:  t.TempDir(), AllowOutsideHome: true,
	}))
	var protocolError *rpc.Error
	if err == nil || !asRPCError(err, &protocolError) || protocolError.Code != rpc.CodeInternal {
		t.Fatalf("runs.spawn of a missing binary = %v, want internal", err)
	}
}

// TestRememberSessionSkipsUnwritableAndNilStores covers the drop-and-note rule:
// no session id or no store records nothing, and a store that cannot write is a
// warning rather than a command failure.
func TestRememberSessionSkipsUnwritableAndNilStores(t *testing.T) {
	rt := handlerRuntime(t)
	rememberSession(rt, state.Session{}) // no id, no store: nothing to write

	st, err := store.Open(filepath.Join(t.TempDir(), "session.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close() // a closed store makes Upsert fail
	rt.Store = st
	rememberSession(rt, state.Session{ID: "sess-closed", Tool: "codex"})
}

// TestSessionOfPrefersTheWireSessionAndFallsBackToEnv pins the precedence:
// the caller's explicit session wins; otherwise a declared environment session
// is used; otherwise there is none.
func TestSessionOfPrefersTheWireSessionAndFallsBackToEnv(t *testing.T) {
	explicit := &state.Session{ID: "explicit", Tool: "cursor"}
	if got := sessionOf(rpc.RunsSpawnParams{Session: explicit}, t.TempDir()); got.ID != "explicit" {
		t.Fatalf("sessionOf(explicit) = %+v, want the wire session", got)
	}

	fromEnv := sessionOf(rpc.RunsSpawnParams{Env: map[string]string{"BERTH_SESSION": "claude-code:env-1"}}, t.TempDir())
	if fromEnv.ID != "env-1" || fromEnv.Tool != "claude-code" {
		t.Fatalf("sessionOf(env) = %+v, want the declared environment session", fromEnv)
	}

	if got := sessionOf(rpc.RunsSpawnParams{}, t.TempDir()); got.ID != "" {
		t.Fatalf("sessionOf(none) = %+v, want no session", got)
	}
}

// TestPortsOfCollectsOnlyOwnedPorts pins the join between a run and the ports
// it holds: a port belongs to the run when it names the same root pid or, for a
// run with an id that survives pid reuse, the same run id, and a duplicate port
// is reported once.
func TestPortsOfCollectsOnlyOwnedPorts(t *testing.T) {
	rec := Record{ID: "run-1", PID: 9001}
	snap := state.Snapshot{Ports: []state.Port{
		{Port: 3000, Run: &state.Run{RootPID: 9001}},
		{Port: 3000, Run: &state.Run{ID: "run-1"}}, // same port, second row: dedup
		{Port: 4000, Run: &state.Run{ID: "run-1"}}, // owned by id, pid reused
		{Port: 5000, Run: &state.Run{RootPID: 9002}},
		{Port: 6000},
	}}
	got := portsOf(rec, snap)
	if len(got) != 2 || got[0] != 3000 || got[1] != 4000 {
		t.Fatalf("portsOf = %v, want [3000 4000]", got)
	}
	if !ownedBy(state.Run{RootPID: 9001}, rec) || !ownedBy(state.Run{ID: "run-1"}, rec) {
		t.Error("ownedBy rejected a port the run owns")
	}
	if ownedBy(state.Run{RootPID: 1}, rec) || ownedBy(state.Run{ID: "other"}, rec) {
		t.Error("ownedBy accepted a port the run does not own")
	}
}

// asRPCError is errors.As without importing errors into every assertion.
func asRPCError(err error, target **rpc.Error) bool {
	value, ok := err.(*rpc.Error)
	if ok {
		*target = value
	}
	return ok
}
