package groupstart

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/codeid"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/runsreg"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// The readiness questions below are answered from two facts that are not the
// scanner's: the run registry knows a no-port worker is alive the instant it is
// registered, and the manifest's health check is a verdict the listener cannot
// stand in for. These two scenarios pin those two answers without a daemon.
func TestPendingWaitsForNoPortDependencyRun(t *testing.T) {
	reg := runsreg.New()
	reg.Mirror = false
	reg.Alive = func(int) bool { return true }
	reg.Register(runsreg.Record{PID: 123, Group: "demo", Name: "worker"})

	rt := &runtimesvc.Runtime{Scanner: scanner.New(scanner.Options{
		Scan: func(scanner.Include) ([]ports.ListeningPort, error) { return nil, nil },
	})}
	rt.SetRuns(reg)
	dep := groups.Service{Name: "worker", Cmd: "worker"}
	want := map[string]int{"worker": 0}

	if err := waitFor(context.Background(), rt, "demo", []groups.Service{dep}, &addressBook{}, ""); err != nil {
		t.Fatalf("waitFor with a live no-port run = %v, want nil", err)
	}
	if got := pending(rt, "demo", []groups.Service{dep}, want); len(got) != 0 {
		t.Fatalf("pending with a live no-port run = %v, want none", got)
	}
	reg.Unregister(123)
	got := pending(rt, "demo", []groups.Service{dep}, want)
	if len(got) != 1 || got[0].Name != "worker" || !strings.Contains(got[0].Display, "worker") {
		t.Fatalf("pending without a live no-port run = %v, want worker", got)
	}
	stale := state.Snapshot{Groups: []state.Group{{
		Name: "demo", Services: []state.Service{{Name: "worker", Running: true}},
	}}}
	if liveService(rt, stale, "demo", "worker") {
		t.Fatal("a stale running row must not override the registry after the worker exits")
	}
	reg.Register(runsreg.Record{PID: 124, Group: "demo", Name: "worker"})
	reg.Alive = func(int) bool { return false }
	if liveService(rt, stale, "demo", "worker") {
		t.Fatal("a dead worker must be pruned before satisfying its dependency")
	}
}

func TestPendingWaitsForConfiguredDependencyHealth(t *testing.T) {
	port := 5432
	path := "/healthz"
	failed := state.Snapshot{Groups: []state.Group{{
		Name: "demo",
		Services: []state.Service{{Name: "db", Running: true, PortActual: &port, Health: &path,
			HealthStatus: &state.Health{Status: state.HealthFail, Reason: "unhealthy"}}},
	}}}
	if healthy(failed, "demo", "db") {
		t.Fatal("failed health verdict was treated as ready")
	}
	ready := state.Snapshot{Groups: []state.Group{{
		Name: "demo",
		Services: []state.Service{{Name: "db", Running: true, PortActual: &port, Health: &path,
			HealthStatus: &state.Health{Status: state.HealthOK}}},
	}}}
	if !healthy(ready, "demo", "db") {
		t.Fatal("healthy verdict was not treated as ready")
	}
}

// TestStartsInDependencyOrderAndWaits is the heart of `oberth up`: db is
// started, api waits for db's port before it is spawned, and every service is
// reported as it happens.
func TestStartsInDependencyOrderAndWaits(t *testing.T) {
	h := newHarness(t)
	dbPort, apiPort := h.freePort(), h.freePort()
	cmd := h.service()
	path := h.manifest(fmt.Sprintf(`name: itest
services:
  - cmd: %s
    name: api
    port: %d
    depends_on:
      - db
  - cmd: %s
    name: db
    port: %d
    ready_timeout: 45s
`, cmd, apiPort, cmd, dbPort))

	reply, chunks, end := h.start(rpc.GroupsStartParams{ConfigPath: &path})
	if !reply.OK || reply.SubscriptionID == "" {
		t.Fatalf("reply = %+v, want ok with a subscription id", reply)
	}
	if got := order(chunks); !slices.Equal(got, []string{"db", "api"}) {
		t.Fatalf("order = %v, want db then api", got)
	}
	wantReadyTimeout := map[string]string{"db": "45s", "api": "30s"}
	for _, chunk := range chunks {
		if chunk.Error != "" || chunk.PID == 0 || chunk.LogPath == "" {
			t.Fatalf("chunk %+v should carry a pid and a log path", chunk)
		}
		if !strings.Contains(chunk.LogPath, filepath.Join("itest", chunk.Service+".log")) {
			t.Errorf("log path %q is not under the group directory", chunk.LogPath)
		}
		if chunk.ReadyTimeout != wantReadyTimeout[chunk.Service] {
			t.Errorf("%s ready_timeout = %q, want %q", chunk.Service, chunk.ReadyTimeout, wantReadyTimeout[chunk.Service])
		}
	}
	if len(end.Started) != 2 || len(end.Errors) != 0 {
		t.Fatalf("end = %+v", end)
	}
	// Each run carries the code identity it started with, and the per-run
	// snapshot it is compared against later lives on disk under that run id.
	for _, chunk := range chunks {
		rec, ok := runsreg.Default.Lookup(chunk.PID)
		if !ok {
			t.Fatalf("run %s was not registered", chunk.Service)
		}
		snap, err := codeid.Load(rec.ID)
		if err != nil {
			t.Fatalf("loading %s's code snapshot: %v", chunk.Service, err)
		}
		if snap.Digest == "" || snap.Root == "" || len(snap.Sources) == 0 {
			t.Fatalf("%s code snapshot = %+v", chunk.Service, snap)
		}
	}
	// api was only spawned once db was actually listening, which is the whole
	// point of depends_on.
	if !dialable(dbPort) {
		t.Error("db is not listening after groups.start reported it started")
	}
}

// sources: [] is the explicit opt-out for a service that reloads its own code;
// it must leave the run without a source snapshot, not pretend the code is
// unchanged.
func TestSourcesEmptyDisablesCodeIdentity(t *testing.T) {
	h := newHarness(t)
	port := h.freePort()
	path := h.manifest(fmt.Sprintf(`name: reload
services:
  - name: api
    cmd: %s
    port: %d
    sources: []
`, h.service(), port))

	chunks, _ := h.startChunks(rpc.GroupsStartParams{ConfigPath: &path})
	if len(chunks) != 1 {
		t.Fatalf("chunks = %+v", chunks)
	}
	rec, ok := runsreg.Default.Lookup(chunks[0].PID)
	if !ok {
		t.Fatal("run was not registered")
	}
	if _, err := codeid.Load(rec.ID); err == nil {
		t.Fatal("sources: [] wrote a source snapshot")
	}
}

// A prepare command is completed before the service process is spawned. This
// is the path compiled artifacts such as Java jars use to refresh the current
// checkout on every `up`/`restart`.
func TestPrepareRunsBeforeServiceAndWritesItsLog(t *testing.T) {
	h := newHarness(t)
	port := h.freePort()
	marker := filepath.Join(h.dir, "prepared.txt")
	path := h.manifest(fmt.Sprintf(`name: prepare
services:
  - name: api
    prepare: /bin/sh -c 'printf built > %s'
    cmd: %s
    port: %d
`, marker, h.service(), port))

	chunks, end := h.startChunks(rpc.GroupsStartParams{ConfigPath: &path})
	if len(chunks) != 1 || len(end.Errors) != 0 || len(end.Started) != 1 {
		t.Fatalf("chunks = %+v, end = %+v", chunks, end)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "built" {
		t.Fatalf("prepare marker = %q, err = %v", data, err)
	}
	if len(chunks[0].LogPath) == 0 {
		t.Fatal("started service has no log path")
	}
	if log, err := os.ReadFile(chunks[0].LogPath); err != nil {
		t.Fatalf("read service log: %v", err)
	} else if !strings.Contains(string(log), "listening on") {
		t.Fatalf("service log does not contain service output: %q", log)
	}
}

func TestPrepareFailureDoesNotStartService(t *testing.T) {
	h := newHarness(t)
	port := h.freePort()
	path := h.manifest(fmt.Sprintf(`name: prepare-fail
services:
  - name: api
    prepare: /bin/sh -c 'echo prepare exploded >&2; exit 3'
    cmd: %s
    port: %d
`, h.service(), port))

	chunks, end := h.startChunks(rpc.GroupsStartParams{ConfigPath: &path})
	if len(chunks) != 1 || len(end.Errors) != 1 || len(end.Started) != 0 {
		t.Fatalf("chunks = %+v, end = %+v; prepare failure must block service", chunks, end)
	}
	failed := chunkFor(t, chunks, "api")
	if failed.Error == "" || !strings.Contains(failed.Error, "prepare failed") {
		t.Fatalf("chunk = %+v, want prepare failure", failed)
	}
	if !strings.Contains(failed.Error, "code 3") {
		t.Fatalf("chunk error = %q, want the prepare exit code", failed.Error)
	}

	// Why the prepare failed lives in the service log, which is what the chunk's
	// hint points a reader at.
	if log, err := os.ReadFile(h.logPath("prepare-fail", "api")); err != nil {
		t.Fatalf("read service log: %v", err)
	} else if !strings.Contains(string(log), "prepare exploded") {
		t.Fatalf("service log = %q, want the prepare output", log)
	}

	// The attempt is durable evidence, and it is not a run: nothing was started,
	// so nothing shows as one.
	exit, ok := runsreg.Default.LatestExit("prepare-fail", "api")
	if !ok || exit.Reason != runsreg.ReasonStartFailed {
		t.Fatalf("exit = %+v/%v, want a start_failed record", exit, ok)
	}
	if len(exit.LastLines) == 0 || !strings.Contains(strings.Join(exit.LastLines, "\n"), "code 3") {
		t.Fatalf("exit lines = %v, want the prepare failure", exit.LastLines)
	}
	if runs := runsreg.Default.GroupRuns("prepare-fail"); len(runs) != 0 {
		t.Fatalf("a failed prepare registered runs: %+v", runs)
	}
}

// TestAutoPortsAreAssignedAndReferenced: two `port: auto` services get ports
// from the claim range, api waits for db on db's assigned port, and api's env
// carries db's port through ${db.port} next to what the caller sent.
func TestAutoPortsAreAssignedAndReferenced(t *testing.T) {
	h := newHarness(t)
	cmd := h.service()
	path := h.manifest(fmt.Sprintf(`name: autotest
services:
  - cmd: %s
    name: api
    port: auto
    depends_on:
      - db
    env:
      DB_URL: postgres://localhost:${db.port}/app
      BERTH_TEST_ECHO: DB_URL,FROM_CALLER,PORT
  - cmd: %s
    name: db
    port: auto
`, cmd, cmd))

	chunks, end := h.startChunks(rpc.GroupsStartParams{
		ConfigPath: &path,
		Env:        map[string]string{"FROM_CALLER": "yes"},
	})
	if len(end.Started) != 2 || len(end.Errors) != 0 {
		t.Fatalf("end = %+v, chunks = %+v", end, chunks)
	}
	if got := order(chunks); !slices.Equal(got, []string{"db", "api"}) {
		t.Fatalf("order = %v, want db then api", got)
	}
	for _, chunk := range chunks {
		if chunk.Port < 10000 || chunk.Port > 32699 {
			t.Errorf("%s port = %d, want one from the claim range", chunk.Service, chunk.Port)
		}
	}
	db, api := chunkFor(t, chunks, "db"), chunkFor(t, chunks, "api")
	if db.Port == api.Port {
		t.Fatalf("db and api were both given %d", db.Port)
	}

	h.waitForLog(api.LogPath,
		fmt.Sprintf("DB_URL=postgres://localhost:%d/app", db.Port),
		"FROM_CALLER=yes",
		fmt.Sprintf("PORT=%d", api.Port),
		fmt.Sprintf("listening on %d", api.Port),
	)
}

// TestWorktreeIdentityExpandsInPrepareAndEnv: ${worktree} is the stable token
// of the checkout holding the manifest, expanded before a service's prepare
// and env run. It names no service, so it adds no dependency. The cmd half is
// the same Expand call and is covered by the groups tests.
func TestWorktreeIdentityExpandsInPrepareAndEnv(t *testing.T) {
	h := newHarness(t)
	token := groups.WorktreeTokenFor(h.dir)
	marker := filepath.Join(h.dir, "prepared.txt")
	path := h.manifest(fmt.Sprintf(`name: worktree-ref
services:
  - name: api
    prepare: /bin/sh -c 'printf %%s ${worktree} > "%s"'
    cmd: %s
    port: auto
    env:
      BERTH_TEST_ECHO: WORKTREE
      WORKTREE: ${worktree}
`, marker, h.service()))

	chunks, end := h.startChunks(rpc.GroupsStartParams{ConfigPath: &path})
	if len(end.Started) != 1 || len(end.Errors) != 0 {
		t.Fatalf("end = %+v, chunks = %+v", end, chunks)
	}
	prepared, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("reading the prepare marker: %v", err)
	}
	if got := strings.TrimSpace(string(prepared)); got != token {
		t.Fatalf("prepare saw ${worktree} = %q, want %q", got, token)
	}
	h.waitForLog(chunkFor(t, chunks, "api").LogPath, "WORKTREE="+token)
}

// TestAnExitIsKeptWithWhereTheRunCameFrom: a service that exits during the
// startup window is reported as a service error and kept among the runs that
// ended, with its code, the last lines it logged, and the config, start and
// client it came from.
func TestAnExitIsKeptWithWhereTheRunCameFrom(t *testing.T) {
	h := newHarness(t)
	path := h.manifest("name: exittest\nservices:\n  - name: job\n    cmd: sh -c 'echo boom; exit 3'\n")

	reply, chunks, end := h.start(rpc.GroupsStartParams{ConfigPath: &path})
	if len(end.Errors) != 1 || len(chunks) != 1 {
		t.Fatalf("end = %+v, chunks = %+v", end, chunks)
	}
	job := chunkFor(t, chunks, "job")
	if job.RunID == "" || job.Error == "" {
		t.Fatalf("chunk = %+v, want a run id and the failure", job)
	}
	if job.State != "failed" || job.Reason != "crashed" || job.Hint != "oberth logs job --once" {
		t.Fatalf("chunk = %+v, want structured crash and log hint", job)
	}
	if reply.StartID == "" {
		t.Fatal("groups.start did not report a start id")
	}

	var exited rpc.RunRecord
	h.eventually(15*time.Second, "the exit in runs.list", func() error {
		var list rpc.RunsListResult
		if err := h.call("runs.list", rpc.Empty{}, &list); err != nil {
			return err
		}
		for _, run := range list.Exited {
			if run.ID == job.RunID {
				exited = run
				return nil
			}
		}
		return fmt.Errorf("run %s is not among the exited runs yet", job.RunID)
	})
	if exited.ExitCode == nil || *exited.ExitCode != 3 || exited.Reason != "crashed" {
		t.Errorf("exit = %+v, want exit code 3 and a crash", exited)
	}
	if len(exited.LastLines) == 0 || exited.LastLines[len(exited.LastLines)-1] != "boom" {
		t.Errorf("last lines = %q, want what the service printed", exited.LastLines)
	}
	if exited.ConfigPath != groups.Canonical(path) || exited.StartID != reply.StartID || exited.Origin != "cli" {
		t.Errorf("run = %+v, want it to name its config, start and client", exited)
	}
	if exited.Status != "exited" {
		t.Errorf("status = %q, want exited", exited.Status)
	}
}

// TestSkipsServicesThatAreAlreadyRunning: `oberth up` is safe to run twice.
func TestSkipsServicesThatAreAlreadyRunning(t *testing.T) {
	h := newHarness(t)
	taken, _ := h.occupiedPort()
	path := h.manifest(fmt.Sprintf(`name: itest
services:
  - cmd: %s
    name: web
    port: %d
`, h.service(), taken))

	chunks, end := h.startChunks(rpc.GroupsStartParams{ConfigPath: &path})
	if len(chunks) != 1 || !chunks[0].Skipped {
		t.Fatalf("chunks = %+v, want one skip", chunks)
	}
	web := chunkFor(t, chunks, "web")
	if web.State != "skipped" {
		t.Errorf("state = %q, want skipped", web.State)
	}
	if web.ReadyTimeout != "30s" {
		t.Errorf("ready_timeout = %q, want the 30s default on a skipped chunk", web.ReadyTimeout)
	}
	if !strings.Contains(web.Reason, "already running") {
		t.Errorf("reason = %q", web.Reason)
	}
	if len(end.Skipped) != 1 || len(end.Started) != 0 {
		t.Fatalf("end = %+v", end)
	}
}

// TestRestartStartsTheServiceItJustStopped is restart's two halves at the RPC
// level: stop the service, then start it again while the old process is still
// finishing its graceful shutdown. The start must begin a new generation, not
// report the process the stop just signalled as "already running" — that skip
// is what left a scoped restart reporting every service skipped and running
// none of them.
func TestRestartStartsTheServiceItJustStopped(t *testing.T) {
	h := newHarness(t)
	t.Setenv(envLinger, "2s")
	port := h.freePort()
	path := h.manifest(fmt.Sprintf(`name: itest
services:
  - cmd: %s
    name: web
    port: %d
`, h.service(), port))

	first, _ := h.startChunks(rpc.GroupsStartParams{ConfigPath: &path})
	if web := chunkFor(t, first, "web"); web.State != "started" {
		t.Fatalf("first start = %+v, want started", web)
	}

	var stopped rpc.KillEnvelope
	if err := h.call("groups.kill", rpc.GroupsKillParams{
		ConfigPath: &path, Only: []string{"web"}, Release: true,
	}, &stopped); err != nil {
		t.Fatalf("groups.kill: %v", err)
	}
	if !stopped.OK {
		t.Fatalf("stop = %+v, want ok", stopped)
	}

	chunks, end := h.startChunks(rpc.GroupsStartParams{ConfigPath: &path})
	web := chunkFor(t, chunks, "web")
	if web.State == "skipped" {
		t.Fatalf("the start after the stop reported %q: %s", web.State, web.Reason)
	}
	if web.State != "started" {
		t.Fatalf("start after stop = %+v, want started", web)
	}
	if !containsString(end.Started, "web") {
		t.Fatalf("end = %+v, want web started", end)
	}
}

// TestReportsADependencyThatExits: a dependency this same start attempted and
// that has already exited can never become ready, so its dependent fails at
// once with the dependency's exit as evidence instead of waiting out the budget.
func TestReportsADependencyThatExits(t *testing.T) {
	h := newHarness(t)
	shortenDependencyWait(t)
	deadPort, livePort := h.freePort(), h.freePort()
	path := h.manifest(fmt.Sprintf(`name: itest
services:
  - cmd: %s
    name: ghost
    port: %d
  - cmd: %s
    name: api
    port: %d
    depends_on:
      - ghost
`, exitsImmediately(t), deadPort, h.service(), livePort))

	chunks, end := h.startChunks(rpc.GroupsStartParams{ConfigPath: &path})
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	api := chunkFor(t, chunks, "api")
	if api.Error == "" {
		t.Fatalf("api chunk = %+v, want a dependency failure", api)
	}
	if !strings.Contains(api.Error, "dependency ghost exited with code") {
		t.Errorf("error = %q, want the dependency's exit", api.Error)
	}
	// The hint names the dependency: `api` never ran, so its own log does not
	// exist and reading it would explain nothing.
	if api.State != "failed" || api.Reason != "dependency_not_ready" || api.Hint != "oberth logs ghost --once" {
		t.Errorf("api chunk = %+v, want dependency_not_ready and the dependency's log hint", api)
	}
	if len(end.Errors) != 2 || !containsString(end.Errors, "ghost") || !containsString(end.Errors, "api") {
		t.Fatalf("end = %+v", end)
	}
}

// TestReportsATrueDependencyTimeout: a dependency that stays alive but never
// listens still gets the full budget, and the dependent reports a timeout.
func TestReportsATrueDependencyTimeout(t *testing.T) {
	h := newHarness(t)
	shortenDependencyWait(t)
	deadPort, livePort := h.freePort(), h.freePort()
	path := h.manifest(fmt.Sprintf(`name: itest
services:
  - cmd: sleep 30
    name: ghost
    port: %d
  - cmd: %s
    name: api
    port: %d
    depends_on:
      - ghost
`, deadPort, h.service(), livePort))

	chunks, end := h.startChunks(rpc.GroupsStartParams{ConfigPath: &path})
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	api := chunkFor(t, chunks, "api")
	if !strings.Contains(api.Error, "timed out") || !strings.Contains(api.Error, "ghost") {
		t.Errorf("error = %q, want it to name the dependency", api.Error)
	}
	if api.State != "failed" || api.Reason != "dependency_timeout" || api.Hint != "oberth logs ghost --once" {
		t.Errorf("api chunk = %+v, want structured timeout and the dependency's log hint", api)
	}
	if !containsString(end.Started, "ghost") || containsString(end.Errors, "ghost") {
		t.Fatalf("end = %+v, want the live dependency started, not failed", end)
	}
	if len(end.Errors) != 1 || !containsString(end.Errors, "api") {
		t.Fatalf("end = %+v, want only the dependent reported failed", end)
	}
}

// A failed worker and a worker omitted by --only must both keep a dependent
// service from starting. No listener exists to make either case ready.
func TestReportsNoPortDependencyTimeout(t *testing.T) {
	for _, tc := range []struct {
		name string
		only []string
	}{
		{name: "failed_worker"},
		{name: "only_api", only: []string{"api"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			shortenDependencyWait(t)
			port := h.freePort()
			path := h.manifest(fmt.Sprintf(`name: itest
services:
  - cmd: %s
    name: worker
  - cmd: %s
    name: api
    port: %d
    depends_on:
      - worker
`, exitsImmediately(t), h.service(), port))

			chunks, end := h.startChunks(rpc.GroupsStartParams{
				ConfigPath: &path, Only: tc.only,
			})
			if len(end.Started) != 0 || !containsString(end.Errors, "api") {
				t.Fatalf("end = %+v, want api to fail without starting", end)
			}
			if len(tc.only) == 0 && !containsString(end.Errors, "worker") {
				t.Fatalf("end = %+v, want the failed worker reported too", end)
			}
			api := chunkFor(t, chunks, "api")
			if api.PID != 0 {
				t.Fatalf("api = %+v, want no process spawned", api)
			}
			if len(tc.only) == 0 {
				// The worker was attempted in this start and exited, so the wait
				// fails on its exit instead of the budget.
				if api.Reason != "dependency_not_ready" ||
					!strings.Contains(api.Error, "dependency worker exited with code") {
					t.Fatalf("api = %+v, want the worker's exit as the dependency failure", api)
				}
			} else {
				// The worker was never attempted, so there is no exit to explain;
				// the wait runs out as before.
				if api.Reason != "dependency_timeout" ||
					!strings.Contains(api.Error, "worker to be running (no port)") {
					t.Fatalf("api = %+v, want a no-port dependency timeout before spawn", api)
				}
			}
			// The hint names the dependency that never became ready. `api` itself
			// never ran, so its own log does not exist — including in the
			// `--only api` case, where the worker was never going to start.
			if api.Hint != "oberth logs worker --once" {
				t.Errorf("hint = %q, want the dependency's log", api.Hint)
			}
			if dialable(port) {
				t.Fatal("api opened its port before the worker was ready")
			}
		})
	}
}

// TestOnlyFiltersAndReportsATypo: --only narrows the plan, and an entry the file
// does not declare is a refusal, not a silent no-op.
func TestOnlyFiltersAndReportsATypo(t *testing.T) {
	h := newHarness(t)
	one, two := h.freePort(), h.freePort()
	cmd := h.service()
	path := h.manifest(fmt.Sprintf(`name: itest
services:
  - cmd: %s
    name: one
    port: %d
  - cmd: %s
    name: two
    port: %d
`, cmd, one, cmd, two))

	chunks, end := h.startChunks(rpc.GroupsStartParams{
		ConfigPath: &path, Only: []string{"two"},
	})
	if len(chunks) != 1 || chunks[0].Service != "two" {
		t.Fatalf("chunks = %+v, want only two", chunks)
	}
	if len(end.Started) != 1 {
		t.Fatalf("end = %+v", end)
	}
	if code := rpcCode(h.startError(rpc.GroupsStartParams{
		ConfigPath: &path, Only: []string{"thre"},
	})); code != "not_found" {
		t.Fatalf("an unknown --only entry answered %q, want not_found", code)
	}
}

// TestDaemonAnnouncesStartDependencyCapability: the CLI feature-detects the
// closure field on this capability, so a daemon serving groups.start must
// announce it.
func TestDaemonAnnouncesStartDependencyCapability(t *testing.T) {
	h := newHarness(t)
	if !slices.Contains(h.client.Hello().Capabilities, rpc.CapabilityStartDependencies) {
		t.Fatalf("capabilities = %v, want %s",
			h.client.Hello().Capabilities, rpc.CapabilityStartDependencies)
	}
}

// TestStartIncludesDependencyClosure: include_dependencies grows only's plan
// with its transitive depends_on closure, and both the chunks and the summary
// say which named service brought each one in.
func TestStartIncludesDependencyClosure(t *testing.T) {
	h := newHarness(t)
	dbPort, apiPort, webPort := h.freePort(), h.freePort(), h.freePort()
	cmd := h.service()
	path := h.manifest(fmt.Sprintf(`name: itest
services:
  - cmd: %s
    name: web
    port: %d
    depends_on:
      - api
  - cmd: %s
    name: api
    port: %d
    depends_on:
      - db
  - cmd: %s
    name: db
    port: %d
`, cmd, webPort, cmd, apiPort, cmd, dbPort))

	chunks, end := h.startChunks(rpc.GroupsStartParams{
		ConfigPath: &path, Only: []string{"web"}, IncludeDependencies: true,
	})
	if len(chunks) != 3 {
		t.Fatalf("chunks = %+v, want db, api and web", chunks)
	}
	if got := order(chunks); !slices.Equal(got, []string{"db", "api", "web"}) {
		t.Fatalf("order = %v, want db, api, web", got)
	}
	for _, name := range []string{"db", "api"} {
		if chunk := chunkFor(t, chunks, name); !slices.Equal(chunk.IncludedBy, []string{"web"}) {
			t.Errorf("%s IncludedBy = %v, want [web]", name, chunk.IncludedBy)
		}
	}
	if web := chunkFor(t, chunks, "web"); web.IncludedBy != nil {
		t.Errorf("web IncludedBy = %v, want nil for the named service", web.IncludedBy)
	}
	if !slices.Equal(end.Included, []string{"api", "db"}) {
		t.Errorf("end.Included = %v, want [api db]", end.Included)
	}
	if len(end.Started) != 3 || len(end.Errors) != 0 {
		t.Fatalf("end = %+v", end)
	}
}

// TestStartWithoutClosureKeepsTheNamedOnly is the restart path: a call that
// does not set include_dependencies still starts exactly what it named.
func TestStartWithoutClosureKeepsTheNamedOnly(t *testing.T) {
	h := newHarness(t)
	one, two := h.freePort(), h.freePort()
	cmd := h.service()
	path := h.manifest(fmt.Sprintf(`name: itest
services:
  - cmd: %s
    name: one
    port: %d
  - cmd: %s
    name: two
    port: %d
`, cmd, one, cmd, two))

	chunks, end := h.startChunks(rpc.GroupsStartParams{
		ConfigPath: &path, Only: []string{"two"},
	})
	if len(chunks) != 1 || chunks[0].Service != "two" {
		t.Fatalf("chunks = %+v, want only two", chunks)
	}
	if chunks[0].IncludedBy != nil {
		t.Errorf("IncludedBy = %v, want none without closure", chunks[0].IncludedBy)
	}
	if len(end.Included) != 0 {
		t.Errorf("end.Included = %v, want none", end.Included)
	}
}

// TestStartNeedsAName rejects a call that names neither a group nor a file.
func TestStartNeedsAName(t *testing.T) {
	h := newHarness(t)
	if code := rpcCode(h.startError(rpc.GroupsStartParams{})); code != "invalid_params" {
		t.Fatalf("answer = %q, want invalid_params", code)
	}
}

// TestEnvFileSuppliesEnvironmentBelowTheManifest: a shared file outside the
// checkout fills the service's environment, its values are not reference
// expanded, and PORT plus the service's own `env:` still win.
func TestEnvFileSuppliesEnvironmentBelowTheManifest(t *testing.T) {
	h := newHarness(t)
	envPath := filepath.Join(h.home, "shared.env")
	if err := os.WriteFile(envPath, []byte("FROM_FILE=file-value\nCALLER_LOSES=file-value\nOVERRIDDEN=file-value\nPORT=9999\nLITERAL=${not-expanded}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	port := h.freePort()
	cmd := h.service()
	path := h.manifest(fmt.Sprintf(`name: envtest
services:
  - name: api
    cmd: %s
    port: %d
    env_file: ../shared.env
    env:
      BERTH_TEST_ECHO: FROM_FILE,CALLER_LOSES,OVERRIDDEN,PORT,LITERAL
      OVERRIDDEN: from-manifest
`, cmd, port))

	chunks, end := h.startChunks(rpc.GroupsStartParams{
		ConfigPath: &path,
		Env:        map[string]string{"CALLER_LOSES": "caller-value"},
	})
	if len(end.Started) != 1 || len(end.Errors) != 0 {
		t.Fatalf("end = %+v, chunks = %+v", end, chunks)
	}
	h.waitForLog(chunkFor(t, chunks, "api").LogPath,
		"FROM_FILE=file-value",
		"CALLER_LOSES=file-value",
		"OVERRIDDEN=from-manifest",
		fmt.Sprintf("PORT=%d", port),
		"LITERAL=${not-expanded}",
	)
}

// TestAddressBookReadsMachineAndWorkspaceReferences: neither is allocated or
// started; a machine reference is its fixed port and a workspace reference is
// the other checkout's declared or observed address.
func TestAddressBookReadsMachineAndWorkspaceReferences(t *testing.T) {
	root := t.TempDir()
	backendDir := filepath.Join(root, "backend")
	if err := os.MkdirAll(backendDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backendDir, groups.ConfigName),
		[]byte("name: backend\nservices:\n  - name: api\n    cmd: run-api\n    port: 8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	frontDir := filepath.Join(root, "frontend")
	if err := os.MkdirAll(frontDir, 0o755); err != nil {
		t.Fatal(err)
	}
	frontPath := filepath.Join(frontDir, groups.ConfigName)
	if err := os.WriteFile(frontPath, []byte(`name: frontend
machine:
  - name: mysql
    port: 3306
workspace:
  - name: backend
    worktree: ../backend
    service: api
services:
  - name: web
    cmd: run-web --db ${mysql.port} --api ${backend.url}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := groups.Load(frontPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b := &addressBook{cfg: cfg, live: map[string]int{}, ports: map[string]int{}, errs: map[string]error{}}

	if port, err := b.port("mysql"); err != nil || port != 3306 {
		t.Fatalf("machine port = (%d, %v), want (3306, nil)", port, err)
	}
	if port, err := b.port("backend"); err != nil || port != 8080 {
		t.Fatalf("workspace port = (%d, %v), want (8080, nil)", port, err)
	}
	ports, err := b.forService(cfg.Services[0])
	if err != nil {
		t.Fatalf("forService: %v", err)
	}
	if ports["mysql"] != 3306 || ports["backend"] != 8080 {
		t.Fatalf("forService ports = %v, want mysql and backend", ports)
	}
}

// shortenDependencyWait gives a scenario a dependency budget it can actually
// wait out. The seam is package-wide, so these scenarios do not run in parallel.
func shortenDependencyWait(t *testing.T) {
	t.Helper()
	old := dependencyTimeout
	dependencyTimeout = 600 * time.Millisecond
	t.Cleanup(func() { dependencyTimeout = old })
}
