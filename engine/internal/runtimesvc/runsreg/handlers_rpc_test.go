package runsreg

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
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
func handlerRuntime(t *testing.T) *runtimesvc.Runtime {
	t.Helper()
	return handlerRuntimeScan(t, nil, func(scanner.Include) ([]ports.ListeningPort, error) { return nil, nil })
}

// handlerRuntimeScan is handlerRuntime with an injected collector and logger,
// for the tests that need to see a snapshot row or a warning.
func handlerRuntimeScan(t *testing.T, logger *slog.Logger, scan func(scanner.Include) ([]ports.ListeningPort, error)) *runtimesvc.Runtime {
	t.Helper()
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	loop := scanner.New(scanner.Options{RuntimeServiceVersion: "test", Logger: logger, Scan: scan})
	return &runtimesvc.Runtime{Version: "test", Logger: logger, Scanner: loop}
}

// handlerRequest frames one call the way the dispatcher does: params under the
// method the handler sees.
func handlerRequest(t *testing.T, rt *runtimesvc.Runtime, method string, params any) *runtimesvc.Request {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshalling %s params: %v", method, err)
	}
	return &runtimesvc.Request{Method: method, ID: json.RawMessage("1"), Params: raw, Runtime: rt}
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
		ok, cast := result.(rpc.OKResult)
		if !cast || !ok.OK {
			t.Fatalf("result = %#v, want OK:true", result)
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

// TestHandleListMarksAHintedRunStartingWhenTheSnapshotIsReadable pins that a
// readable listener table is a fact: a port-hinted run whose port is not in it
// is "starting", not "running". It kills the mutant that flips the readability
// verdict.
func TestHandleListMarksAHintedRunStartingWhenTheSnapshotIsReadable(t *testing.T) {
	resetRegistry(t)
	rt := handlerRuntime(t)
	Default.Register(Record{ID: "run-starting", PID: os.Getpid(), PortHint: 3000})

	result, err := handleList(context.Background(), handlerRequest(t, rt, "runs.list", struct{}{}))
	if err != nil {
		t.Fatalf("runs.list: %v", err)
	}
	list := result.(rpc.RunsListResult)
	if len(list.Runs) != 1 || list.Runs[0].Status != "starting" {
		t.Fatalf("rows = %+v, want the hinted run starting", list.Runs)
	}
}

// TestHandleListJoinsObservedPorts pins that a readable snapshot is actually
// used: the port observed for the run appears on its row. It kills the mutant
// that discards the snapshot on success.
func TestHandleListJoinsObservedPorts(t *testing.T) {
	resetRegistry(t)
	pid := os.Getpid()
	rt := handlerRuntimeScan(t, nil, func(scanner.Include) ([]ports.ListeningPort, error) {
		return []ports.ListeningPort{{Port: 3000, BindAddress: "127.0.0.1", PID: pid, RunID: "run-live", RunRootPID: pid}}, nil
	})
	Default.Register(Record{ID: "run-live", PID: pid, PortHint: 3000})

	result, err := handleList(context.Background(), handlerRequest(t, rt, "runs.list", struct{}{}))
	if err != nil {
		t.Fatalf("runs.list: %v", err)
	}
	list := result.(rpc.RunsListResult)
	if len(list.Runs) != 1 || len(list.Runs[0].Ports) != 1 || list.Runs[0].Ports[0] != 3000 {
		t.Fatalf("rows = %+v, want the observed port 3000", list.Runs)
	}
	if list.Runs[0].Status != "running" {
		t.Fatalf("status = %q, want running for a listening hinted port", list.Runs[0].Status)
	}
}

// TestRowAndStatusHonourThePortHintBoundary pins the zero/one boundary of the
// hint: only a positive hint is published, and the status rule reads the same
// boundary. It kills the >= and 0->1 mutants on both.
func TestRowAndStatusHonourThePortHintBoundary(t *testing.T) {
	noHint := row(Record{ID: "r", PID: 1, PortHint: 0}, state.Snapshot{}, true)
	if noHint.PortHint != nil || noHint.URL != "" {
		t.Errorf("zero hint = %+v, want no hint and no url", noHint)
	}
	one := row(Record{ID: "r", PID: 1, PortHint: 1}, state.Snapshot{}, true)
	if one.PortHint == nil || *one.PortHint != 1 {
		t.Errorf("hint 1 = %+v, want the hint kept", one)
	}
	if got := runStatus(Record{PortHint: 1}, nil, true); got != "starting" {
		t.Errorf("runStatus(hint 1, known, absent) = %q, want starting", got)
	}
	if got := runStatus(Record{PortHint: 0}, nil, true); got != "running" {
		t.Errorf("runStatus(no hint, known) = %q, want running", got)
	}
}

// TestRequirePIDKeepsAPositivePID pins the boundary: pid 1 is a valid pid and
// only a non-positive one is refused.
func TestRequirePIDKeepsAPositivePID(t *testing.T) {
	if err := requirePID(1, "example"); err != nil {
		t.Fatalf("requirePID(1) = %v, want nil", err)
	}
	if err := requirePID(0, "example"); err == nil {
		t.Fatal("requirePID(0) = nil, want a refusal")
	}
}

// TestRememberSessionDropsOnlyWhatItCannotWrite pins the early exits: an empty
// id, a nil runtime or a runtime without a store records nothing and does not
// dereference what is absent.
func TestRememberSessionDropsOnlyWhatItCannotWrite(t *testing.T) {
	rt := handlerRuntime(t) // Store is nil
	rememberSession(rt, state.Session{ID: "sess-no-store", Tool: "codex"})
	rememberSession(nil, state.Session{ID: "sess-no-runtime", Tool: "codex"})
	rememberSession(rt, state.Session{})
	if len(Default.List()) != 0 {
		t.Fatal("rememberSession touched the run registry")
	}
}

// TestRememberSessionWarnsOnlyWhenTheWriteFails pins the diagnostic rule: a
// failed write is reported once and a successful one is silent. It kills the
// mutant that warns on success.
func TestRememberSessionWarnsOnlyWhenTheWriteFails(t *testing.T) {
	t.Run("success is silent", func(t *testing.T) {
		var log bytes.Buffer
		rt := handlerRuntimeScan(t, slog.New(slog.NewTextHandler(&log, nil)), func(scanner.Include) ([]ports.ListeningPort, error) { return nil, nil })
		st, err := store.Open(filepath.Join(t.TempDir(), "quiet.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		rt.Store = st
		rememberSession(rt, state.Session{ID: "sess-ok", Tool: "codex"})
		if log.Len() != 0 {
			t.Fatalf("a successful session write logged %q, want silence", log.String())
		}
	})
	t.Run("failure warns", func(t *testing.T) {
		var log bytes.Buffer
		rt := handlerRuntimeScan(t, slog.New(slog.NewTextHandler(&log, nil)), func(scanner.Include) ([]ports.ListeningPort, error) { return nil, nil })
		st, err := store.Open(filepath.Join(t.TempDir(), "closed.db"))
		if err != nil {
			t.Fatal(err)
		}
		_ = st.Close()
		rt.Store = st
		rememberSession(rt, state.Session{ID: "sess-closed", Tool: "codex"})
		if !strings.Contains(log.String(), "recording an agent session") {
			t.Fatalf("a failed session write logged %q, want the recording warning", log.String())
		}
	})
}

// TestHandleSpawnStartsAndReapsARun pins the success path end to end: the reply
// carries an ok mutation and the new run's id, and the runtime service reaps the process
// into exit history. The test waits for the reaper before it lets the registry
// be restored, so the reaping goroutine cannot race the cleanup.
//
// The exit the reaper recorded is checked for the facts only a detached spawn
// can produce: a natural zero exit is "exited" rather than a stop option-berth
// asked for, and the log the child's output went to is a real file. Removing
// the detach at the spawn entry keeps the run reaped and the id intact — the
// old assertions still pass — so this is the shape that tells the two apart.
func TestHandleSpawnStartsAndReapsARun(t *testing.T) {
	resetRegistry(t)
	rt := handlerRuntime(t)
	// The child has to be an executable this machine certainly has: the test
	// binary itself, run with a -test.run pattern that matches no test, so it
	// starts, exits at once and leaves nothing behind.
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	result, err := handleSpawn(context.Background(), handlerRequest(t, rt, "runs.spawn", rpc.RunsSpawnParams{
		Argv: []string{self, "-test.run=^$"}, Cwd: t.TempDir(), AllowOutsideHome: true,
	}))
	if err != nil {
		t.Fatalf("runs.spawn: %v", err)
	}
	spawned, ok := result.(rpc.RunsSpawnResult)
	if !ok || spawned.RunID == "" {
		t.Fatalf("result = %#v, want a spawned run with an id", result)
	}
	if !spawned.MutationResult.OK {
		t.Error("the spawn reply did not report ok")
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, exit := range Default.Exits() {
			if exit.ID == spawned.RunID {
				// The process ended on its own with success, which is what
				// "exited" means; a stop the runtime service initiated is a different
				// reason and would hide a spawn that never detached.
				if exit.Code != 0 || exit.Reason != ReasonExited {
					t.Fatalf("natural exit = %+v", exit)
				}
				// A detached run has no caller to hold its output, so the
				// registry's own record has to name the file the child wrote.
				if exit.LogPath == "" {
					t.Fatal("detached run has no observed log path")
				}
				if info, err := os.Stat(exit.LogPath); err != nil || !info.Mode().IsRegular() {
					t.Fatalf("detached log %q: stat=%v error=%v", exit.LogPath, info, err)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the spawned run was never reaped into exit history")
}

// asRPCError is errors.As without importing errors into every assertion.
func asRPCError(err error, target **rpc.Error) bool {
	value, ok := err.(*rpc.Error)
	if ok {
		*target = value
	}
	return ok
}
