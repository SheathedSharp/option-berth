package groupstart

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/codeid"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/runsreg"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/testenv"
)

// The child side of every test here: re-running this binary with
// BERTH_TEST_LISTEN set makes it a tiny service that binds the port option-berth told
// it to and waits. Using the test binary rather than a compiled helper keeps
// the test hermetic and works the same on every OS.
const (
	envListen = "BERTH_TEST_LISTEN"
	envDelay  = "BERTH_TEST_LISTEN_DELAY"
	// envEcho names variables, comma separated, the child prints as NAME=value
	// before it listens, so a test can read the environment a service got.
	envEcho = "BERTH_TEST_ECHO"
)

// TestMain doubles as the service a group starts. A service is not a test run,
// so only the test branch is isolated; the child inherits the environment its
// parent was already isolated into.
func TestMain(m *testing.M) {
	if os.Getenv(envListen) == "" {
		os.Exit(testenv.Run(m))
	}
	if d, err := time.ParseDuration(os.Getenv(envDelay)); err == nil && d > 0 {
		time.Sleep(d)
	}
	for _, name := range strings.Split(os.Getenv(envEcho), ",") {
		if name != "" {
			fmt.Printf("%s=%s\n", name, os.Getenv(name))
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("BERTH_PORT"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer ln.Close()
	fmt.Println("listening on", os.Getenv("BERTH_PORT"))
	// Accept forever. `select {}` would be a deadlock the runtime panics on;
	// blocking on the listener is what a real service does anyway.
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		conn.Close()
	}
}

// serviceCmd is the `cmd:` a test writes into a `.oberth.yaml`: this very test
// binary, in listen mode.
func serviceCmd(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Single-quoted for YAML, double-quoted for the argv splitter: the value
	// is one scalar that splits into [binary, -test.run=...].
	return fmt.Sprintf("'%q -test.run=TestNothing'", self)
}

// dumpLogs prints what the started services wrote, so a failure says why the
// child did not come up instead of only that it did not.
func dumpLogs(t *testing.T, chunks []rpc.GroupsStartChunk) {
	t.Helper()
	for _, c := range chunks {
		if c.LogPath == "" {
			continue
		}
		if raw, err := os.ReadFile(c.LogPath); err == nil {
			t.Logf("%s log:\n%s", c.Service, raw)
		}
	}
}

// freePort returns a port nothing is listening on right now.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// probeScan is the daemon's scan in these tests: it dials the ports the test
// cares about and reports the ones that answer, attributed to dir. Real
// sockets, deterministic result.
func probeScan(dir string, watch []int) func(scanner.Include) ([]ports.ListeningPort, error) {
	return func(scanner.Include) ([]ports.ListeningPort, error) {
		var out []ports.ListeningPort
		for _, port := range watch {
			conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 100*time.Millisecond)
			if err != nil {
				continue
			}
			conn.Close()
			out = append(out, ports.ListeningPort{
				Port: port, BindAddress: "127.0.0.1", IPVersion: "ipv4",
				PID: os.Getpid(), Process: "listener", Cwd: dir,
			})
		}
		return out, nil
	}
}

// startDaemon runs a real daemon on a temp socket with the given scan, and
// returns a connected client.
func startDaemon(t *testing.T, ctx context.Context, dir string, watch []int) *client.Client {
	t.Helper()
	return startDaemonWith(t, ctx, probeScan(dir, watch))
}

// watchList is a scan's watch list that grows while a test runs: a `port:
// auto` service's port is only known once groups.start reports it.
type watchList struct {
	mu   sync.Mutex
	pids map[int]int // port -> pid of the service listening on it
}

func (w *watchList) add(port, pid int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pids == nil {
		w.pids = map[int]int{}
	}
	w.pids[port] = pid
}

// watchScan dials every watched port and reports the ones that answer,
// attributed to the service's own pid so the run registry claims them the way
// it claims a real listener in a service's process tree.
func watchScan(dir string, w *watchList) func(scanner.Include) ([]ports.ListeningPort, error) {
	return func(scanner.Include) ([]ports.ListeningPort, error) {
		w.mu.Lock()
		watched := make(map[int]int, len(w.pids))
		for port, pid := range w.pids {
			watched[port] = pid
		}
		w.mu.Unlock()
		var out []ports.ListeningPort
		for port, pid := range watched {
			if !dialable(port) {
				continue
			}
			out = append(out, ports.ListeningPort{
				Port: port, BindAddress: "127.0.0.1", IPVersion: "ipv4",
				PID: pid, Process: "listener", Cwd: dir,
			})
		}
		return out, nil
	}
}

// startDaemonWith is startDaemon with the scan supplied by the caller.
func startDaemonWith(t *testing.T, ctx context.Context, scan func(scanner.Include) ([]ports.ListeningPort, error)) *client.Client {
	t.Helper()
	// A unix socket path is capped at ~104 bytes, and t.TempDir() spells the
	// test's name into the path, so the socket gets its own short directory.
	sockDir, err := os.MkdirTemp("", "sn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	socket := filepath.Join(sockDir, "d.sock")

	srv := daemon.New(daemon.Options{
		Socket:  socket,
		Version: "test",
		DBPath:  filepath.Join(sockDir, "option-berth.db"),
		Scanner: scanner.New(scanner.Options{
			DaemonVersion: "test",
			Scan:          scan,
		}),
	})
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		srv.Shutdown()
		<-srv.Done()
	})

	var c *client.Client
	for i := 0; i < 100; i++ {
		c, err = client.Dial(ctx, client.ClientInfo{Name: "cli", Version: "test", Socket: socket})
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		select {
		case serr := <-serveErr:
			t.Fatalf("the test daemon did not start: %v (dial: %v)", serr, err)
		default:
		}
		t.Fatalf("connecting to the test daemon: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// isolate points HOME, the log directory and the run mirror at a temp tree, so
// a test never writes into the developer's real config. It returns a project
// directory *inside* that home: the daemon refuses to start a command outside
// the user's home unless asked to, and these tests should exercise the ordinary
// path rather than the opt-out.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("BERTH_LOG_DIR", filepath.Join(home, "logs"))
	t.Setenv(envListen, "1")

	dir := filepath.Join(home, "project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// collect drains a groups.start stream into its chunks and its end payload.
func collect(t *testing.T, s *client.Stream) ([]rpc.GroupsStartChunk, rpc.GroupsStartEnd) {
	t.Helper()
	var chunks []rpc.GroupsStartChunk
	done := make(chan struct{})
	go func() {
		defer close(done)
		for raw := range s.Chunks() {
			var c rpc.GroupsStartChunk
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Errorf("decoding a chunk: %v", err)
				continue
			}
			chunks = append(chunks, c)
		}
	}()

	select {
	case end := <-s.End():
		<-done
		if end.Err != nil {
			t.Fatalf("stream ended with an error: %v", end.Err)
		}
		var summary rpc.GroupsStartEnd
		if err := end.Decode(&summary); err != nil {
			t.Fatalf("decoding the end payload: %v", err)
		}
		return chunks, summary
	case <-time.After(60 * time.Second):
		t.Fatal("groups.start never ended")
		return nil, rpc.GroupsStartEnd{}
	}
}

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, groups.ConfigName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func skipOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test binary re-exec helper and unix socket are not set up for Windows here")
	}
}

// TestNothing is the -test.run target the spawned child uses; TestMain turns
// into a listener before it would ever run.
func TestNothing(t *testing.T) {}

func TestPendingWaitsForNoPortDependencyRun(t *testing.T) {
	reg := runsreg.New()
	reg.Mirror = false
	reg.Alive = func(int) bool { return true }
	reg.Register(runsreg.Record{PID: 123, Group: "demo", Name: "worker"})

	rt := &daemon.Runtime{Scanner: scanner.New(scanner.Options{
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
	skipOnWindows(t)
	dir := isolate(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbPort, apiPort := freePort(t), freePort(t)
	cmd := serviceCmd(t)
	path := writeConfig(t, dir, fmt.Sprintf(`name: itest
services:
  - name: api
    cmd: %s
    port: %d
    depends_on: [db]
  - name: db
    cmd: %s
    port: %d
`, cmd, apiPort, cmd, dbPort))

	c := startDaemon(t, ctx, dir, []int{dbPort, apiPort})

	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{ConfigPath: &path}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	defer s.Close()
	if !start.OK || start.SubscriptionID == "" {
		t.Fatalf("reply = %+v, want ok with a subscription id", start)
	}

	chunks, end := collect(t, s)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v, want one per service", chunks)
	}
	if chunks[0].Service != "db" || chunks[1].Service != "api" {
		t.Fatalf("order = %s, %s; want db then api", chunks[0].Service, chunks[1].Service)
	}
	for _, ch := range chunks {
		if ch.Error != "" || ch.PID == 0 || ch.LogPath == "" {
			dumpLogs(t, chunks)
			t.Fatalf("chunk %+v should carry a pid and a log path", ch)
		}
		if !strings.Contains(ch.LogPath, filepath.Join("itest", ch.Service+".log")) {
			t.Errorf("log path %q is not under the group directory", ch.LogPath)
		}
	}
	if len(end.Started) != 2 || len(end.Errors) != 0 {
		t.Fatalf("end = %+v", end)
	}
	// Each run carries the code identity it started with, and the per-run
	// snapshot it is compared against later lives on disk under that run id.
	for _, ch := range chunks {
		rec, ok := runsreg.Default.Lookup(ch.PID)
		if !ok {
			t.Fatalf("run %s was not registered", ch.Service)
		}
		snap, err := codeid.Load(rec.ID)
		if err != nil {
			t.Fatalf("loading %s's code snapshot: %v", ch.Service, err)
		}
		if snap.Digest == "" || snap.Root == "" || len(snap.Sources) == 0 {
			t.Fatalf("%s code snapshot = %+v", ch.Service, snap)
		}
	}

	// api was only spawned once db was actually listening, which is the whole
	// point of depends_on.
	if !dialable(dbPort) {
		t.Error("db is not listening after groups.start reported it started")
	}

	t.Cleanup(func() { killPIDs(chunks) })
}

// sources: [] is the explicit opt-out for a service that reloads its own code;
// it must leave the run without a source snapshot, not pretend the code is
// unchanged.
func TestSourcesEmptyDisablesCodeIdentity(t *testing.T) {
	skipOnWindows(t)
	dir := isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := freePort(t)
	path := writeConfig(t, dir, fmt.Sprintf(`name: reload
services:
  - name: api
    cmd: %s
    port: %d
    sources: []
`, serviceCmd(t), port))
	c := startDaemon(t, ctx, dir, []int{port})
	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{ConfigPath: &path}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	defer s.Close()
	chunks, _ := collect(t, s)
	if len(chunks) != 1 {
		t.Fatalf("chunks = %+v", chunks)
	}
	t.Cleanup(func() { killPIDs(chunks) })
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
	skipOnWindows(t)
	dir := isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := freePort(t)
	marker := filepath.Join(dir, "prepared.txt")
	path := writeConfig(t, dir, fmt.Sprintf(`name: prepare
services:
  - name: api
    prepare: /bin/sh -c 'printf built > %s'
    cmd: %s
    port: %d
`, marker, serviceCmd(t), port))
	c := startDaemon(t, ctx, dir, []int{port})
	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{ConfigPath: &path}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	chunks, end := collect(t, s)
	if len(chunks) != 1 || len(end.Errors) != 0 || len(end.Started) != 1 {
		dumpLogs(t, chunks)
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
	t.Cleanup(func() { killPIDs(chunks) })
}

func TestPrepareFailureDoesNotStartService(t *testing.T) {
	skipOnWindows(t)
	dir := isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	port := freePort(t)
	path := writeConfig(t, dir, fmt.Sprintf(`name: prepare-fail
services:
  - name: api
    prepare: /bin/sh -c 'echo prepare exploded >&2; exit 3'
    cmd: %s
    port: %d
`, serviceCmd(t), port))
	c := startDaemon(t, ctx, dir, []int{port})
	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{ConfigPath: &path}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	chunks, end := collect(t, s)
	if len(chunks) != 1 || len(end.Errors) != 1 || len(end.Started) != 0 {
		t.Fatalf("chunks = %+v, end = %+v; prepare failure must block service", chunks, end)
	}
	if chunks[0].Error == "" || !strings.Contains(chunks[0].Error, "prepare failed") {
		t.Fatalf("chunk = %+v, want prepare failure", chunks[0])
	}
	if !strings.Contains(chunks[0].Error, "code 3") {
		t.Fatalf("chunk error = %q, want the prepare exit code", chunks[0].Error)
	}

	// Why the prepare failed lives in the service log, which is what the
	// chunk's hint points a reader at.
	logPath := filepath.Join(os.Getenv("BERTH_LOG_DIR"), "prepare-fail", "api.log")
	if log, err := os.ReadFile(logPath); err != nil {
		t.Fatalf("read service log: %v", err)
	} else if !strings.Contains(string(log), "prepare exploded") {
		t.Fatalf("service log = %q, want the prepare output", log)
	}

	// The attempt is durable evidence, and it is not a run: nothing was
	// started, so nothing shows as one.
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
	skipOnWindows(t)
	dir := isolate(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := serviceCmd(t)
	path := writeConfig(t, dir, fmt.Sprintf(`name: autotest
services:
  - name: api
    cmd: %s
    port: auto
    depends_on: [db]
    env:
      BERTH_TEST_ECHO: DB_URL,FROM_CALLER,PORT
      DB_URL: postgres://localhost:${db.port}/app
  - name: db
    cmd: %s
    port: auto
`, cmd, cmd))

	watch := &watchList{}
	c := startDaemonWith(t, ctx, watchScan(dir, watch))

	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{
		ConfigPath: &path,
		Env:        map[string]string{"FROM_CALLER": "yes"},
	}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	defer s.Close()

	chunks, end := collectWatching(t, s, watch)
	t.Cleanup(func() { killPIDs(chunks) })
	if len(end.Started) != 2 || len(end.Errors) != 0 {
		dumpLogs(t, chunks)
		t.Fatalf("end = %+v, chunks = %+v", end, chunks)
	}
	db, api := chunks[0], chunks[1]
	if db.Service != "db" || api.Service != "api" {
		t.Fatalf("order = %s, %s; want db then api", db.Service, api.Service)
	}
	for _, ch := range chunks {
		if ch.Port < 10000 || ch.Port > 32699 {
			t.Errorf("%s port = %d, want one from the claim range", ch.Service, ch.Port)
		}
	}
	if db.Port == api.Port {
		t.Fatalf("db and api were both given %d", db.Port)
	}

	want := []string{
		fmt.Sprintf("DB_URL=postgres://localhost:%d/app", db.Port),
		"FROM_CALLER=yes",
		fmt.Sprintf("PORT=%d", api.Port),
		fmt.Sprintf("listening on %d", api.Port),
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, _ := os.ReadFile(api.LogPath)
		missing := ""
		for _, w := range want {
			if !strings.Contains(string(raw), w) {
				missing = w
				break
			}
		}
		if missing == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("api log is missing %q:\n%s", missing, raw)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestWorktreeIdentityExpandsInPrepareAndEnv: ${worktree} is the stable token
// of the checkout holding the manifest, expanded before a service's prepare
// and env run. It names no service, so it adds no dependency. The cmd half is
// the same Expand call and is covered by the groups tests.
func TestWorktreeIdentityExpandsInPrepareAndEnv(t *testing.T) {
	skipOnWindows(t)
	dir := isolate(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	token := groups.WorktreeTokenFor(dir)
	marker := filepath.Join(dir, "prepared.txt")
	cmd := serviceCmd(t)
	path := writeConfig(t, dir, fmt.Sprintf(`name: worktree-ref
services:
  - name: api
    prepare: /bin/sh -c 'printf %%s ${worktree} > "%s"'
    cmd: %s
    port: auto
    env:
      BERTH_TEST_ECHO: WORKTREE
      WORKTREE: ${worktree}
`, marker, cmd))

	watch := &watchList{}
	c := startDaemonWith(t, ctx, watchScan(dir, watch))

	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{ConfigPath: &path}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	defer s.Close()

	chunks, end := collectWatching(t, s, watch)
	t.Cleanup(func() { killPIDs(chunks) })
	if len(end.Started) != 1 || len(end.Errors) != 0 {
		dumpLogs(t, chunks)
		t.Fatalf("end = %+v, chunks = %+v", end, chunks)
	}

	prepared, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("reading the prepare marker: %v", err)
	}
	if got := strings.TrimSpace(string(prepared)); got != token {
		t.Fatalf("prepare saw ${worktree} = %q, want %q", got, token)
	}

	want := "WORKTREE=" + token
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, _ := os.ReadFile(chunks[0].LogPath)
		if strings.Contains(string(raw), want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("api log is missing %q:\n%s", want, raw)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// collectWatching is collect for a scan whose watch list grows: every service
// reported started is added to it, so the next scan sees its port.
func collectWatching(t *testing.T, s *client.Stream, w *watchList) ([]rpc.GroupsStartChunk, rpc.GroupsStartEnd) {
	t.Helper()
	var chunks []rpc.GroupsStartChunk
	done := make(chan struct{})
	go func() {
		defer close(done)
		for raw := range s.Chunks() {
			var c rpc.GroupsStartChunk
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Errorf("decoding a chunk: %v", err)
				continue
			}
			if c.Port > 0 && c.PID > 0 {
				w.add(c.Port, c.PID)
			}
			chunks = append(chunks, c)
		}
	}()
	select {
	case end := <-s.End():
		<-done
		if end.Err != nil {
			t.Fatalf("stream ended with an error: %v", end.Err)
		}
		var summary rpc.GroupsStartEnd
		if err := end.Decode(&summary); err != nil {
			t.Fatalf("decoding the end payload: %v", err)
		}
		return chunks, summary
	case <-time.After(60 * time.Second):
		t.Fatal("groups.start never ended")
		return nil, rpc.GroupsStartEnd{}
	}
}

// TestAnExitIsKeptWithWhereTheRunCameFrom: a service that exits during the
// startup window is reported as a service error and kept among the runs that
// ended, with its code, the last lines it logged, and the config, start and
// client it came from.
func TestAnExitIsKeptWithWhereTheRunCameFrom(t *testing.T) {
	skipOnWindows(t)
	dir := isolate(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	path := writeConfig(t, dir, "name: exittest\nservices:\n  - name: job\n    cmd: sh -c 'echo boom; exit 3'\n")
	c := startDaemon(t, ctx, dir, nil)

	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{ConfigPath: &path}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	defer s.Close()
	chunks, end := collect(t, s)
	if len(end.Errors) != 1 || len(chunks) != 1 || chunks[0].RunID == "" || chunks[0].Error == "" {
		t.Fatalf("end = %+v, chunks = %+v", end, chunks)
	}
	if chunks[0].State != "failed" || chunks[0].Reason != "crashed" || chunks[0].Hint != "oberth logs job --once" {
		t.Fatalf("chunk = %+v, want structured crash and log hint", chunks[0])
	}
	if start.StartID == "" {
		t.Fatal("groups.start did not report a start id")
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		var list rpc.RunsListResult
		if err := c.Call(ctx, "runs.list", rpc.Empty{}, &list); err != nil {
			t.Fatalf("runs.list: %v", err)
		}
		for _, r := range list.Exited {
			if r.ID != chunks[0].RunID {
				continue
			}
			if r.ExitCode == nil || *r.ExitCode != 3 || r.Reason != "crashed" {
				t.Errorf("exit = %+v, want exit code 3 and a crash", r)
			}
			if len(r.LastLines) == 0 || r.LastLines[len(r.LastLines)-1] != "boom" {
				t.Errorf("last lines = %q, want what the service printed", r.LastLines)
			}
			if r.ConfigPath != groups.Canonical(path) || r.StartID != start.StartID || r.Origin != "cli" {
				t.Errorf("run = %+v, want it to name its config, start and client", r)
			}
			if r.Status != "exited" {
				t.Errorf("status = %q, want exited", r.Status)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the exit never reached runs.list: %+v", list)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestSkipsServicesThatAreAlreadyRunning: `oberth up` is safe to run twice.
func TestSkipsServicesThatAreAlreadyRunning(t *testing.T) {
	skipOnWindows(t)
	dir := isolate(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Something else is already on the port the service declares.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	taken := ln.Addr().(*net.TCPAddr).Port

	path := writeConfig(t, dir, fmt.Sprintf(`name: itest
services:
  - name: web
    cmd: %s
    port: %d
`, serviceCmd(t), taken))

	c := startDaemon(t, ctx, dir, []int{taken})

	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{ConfigPath: &path}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	defer s.Close()

	chunks, end := collect(t, s)
	if len(chunks) != 1 || !chunks[0].Skipped {
		t.Fatalf("chunks = %+v, want one skip", chunks)
	}
	if chunks[0].State != "skipped" {
		t.Errorf("state = %q, want skipped", chunks[0].State)
	}
	if !strings.Contains(chunks[0].Reason, "already running") {
		t.Errorf("reason = %q", chunks[0].Reason)
	}
	if len(end.Skipped) != 1 || len(end.Started) != 0 {
		t.Fatalf("end = %+v", end)
	}
}

// TestReportsADependencyThatExits: a dependency this same start attempted and
// that has already exited can never become ready, so its dependent fails at
// once with the dependency's exit as evidence instead of waiting out the budget.
func TestReportsADependencyThatExits(t *testing.T) {
	skipOnWindows(t)
	dir := isolate(t)

	old := dependencyTimeout
	dependencyTimeout = 600 * time.Millisecond
	t.Cleanup(func() { dependencyTimeout = old })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deadPort, livePort := freePort(t), freePort(t)
	path := writeConfig(t, dir, fmt.Sprintf(`name: itest
services:
  - name: ghost
    cmd: %s
    port: %d
  - name: api
    cmd: %s
    port: %d
    depends_on: [ghost]
`, exitsImmediately(t), deadPort, serviceCmd(t), livePort))

	c := startDaemon(t, ctx, dir, []int{deadPort, livePort})

	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{ConfigPath: &path}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	defer s.Close()

	chunks, end := collect(t, s)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	api := chunks[1]
	if api.Service != "api" || api.Error == "" {
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
	skipOnWindows(t)
	dir := isolate(t)

	old := dependencyTimeout
	dependencyTimeout = 600 * time.Millisecond
	t.Cleanup(func() { dependencyTimeout = old })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deadPort, livePort := freePort(t), freePort(t)
	path := writeConfig(t, dir, fmt.Sprintf(`name: itest
services:
  - name: ghost
    cmd: sleep 30
    port: %d
  - name: api
    cmd: %s
    port: %d
    depends_on: [ghost]
`, deadPort, serviceCmd(t), livePort))

	c := startDaemon(t, ctx, dir, []int{deadPort, livePort})

	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{ConfigPath: &path}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	defer s.Close()

	chunks, end := collect(t, s)
	t.Cleanup(func() { killPIDs(chunks) })
	if len(chunks) != 2 {
		t.Fatalf("chunks = %+v", chunks)
	}
	api := chunks[1]
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
	skipOnWindows(t)
	old := dependencyTimeout
	dependencyTimeout = 600 * time.Millisecond
	t.Cleanup(func() { dependencyTimeout = old })

	for _, tc := range []struct {
		name string
		only []string
	}{
		{name: "failed_worker"},
		{name: "only_api", only: []string{"api"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := isolate(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			port := freePort(t)
			path := writeConfig(t, dir, fmt.Sprintf(`name: itest
services:
  - name: worker
    cmd: %s
  - name: api
    cmd: %s
    port: %d
    depends_on: [worker]
`, exitsImmediately(t), serviceCmd(t), port))
			c := startDaemon(t, ctx, dir, []int{port})
			var start rpc.GroupsStartResult
			s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{
				ConfigPath: &path, Only: tc.only,
			}, &start)
			if err != nil {
				t.Fatalf("groups.start: %v", err)
			}
			defer s.Close()
			chunks, end := collect(t, s)
			t.Cleanup(func() { killPIDs(chunks) })
			if len(end.Started) != 0 || !containsString(end.Errors, "api") {
				t.Fatalf("end = %+v, want api to fail without starting", end)
			}
			if len(tc.only) == 0 && !containsString(end.Errors, "worker") {
				t.Fatalf("end = %+v, want the failed worker reported too", end)
			}
			api := chunks[len(chunks)-1]
			if api.Service != "api" || api.PID != 0 {
				t.Fatalf("api = %+v, want no process spawned", api)
			}
			if len(tc.only) == 0 {
				// The worker was attempted in this start and exited, so the
				// wait fails on its exit instead of the budget.
				if api.Reason != "dependency_not_ready" ||
					!strings.Contains(api.Error, "dependency worker exited with code") {
					t.Fatalf("api = %+v, want the worker's exit as the dependency failure", api)
				}
			} else {
				// The worker was never attempted, so there is no exit to
				// explain; the wait runs out as before.
				if api.Reason != "dependency_timeout" ||
					!strings.Contains(api.Error, "worker to be running (no port)") {
					t.Fatalf("api = %+v, want a no-port dependency timeout before spawn", api)
				}
			}
			// The hint names the dependency that never became ready. `api`
			// itself never ran, so its own log does not exist — including in
			// the `--only api` case, where the worker was never going to start.
			if api.Hint != "oberth logs worker --once" {
				t.Errorf("hint = %q, want the dependency's log", api.Hint)
			}
			if dialable(port) {
				t.Fatal("api opened its port before the worker was ready")
			}
		})
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestOnlyFiltersAndReportsATypo.
func TestOnlyFiltersAndReportsATypo(t *testing.T) {
	skipOnWindows(t)
	dir := isolate(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	one, two := freePort(t), freePort(t)
	cmd := serviceCmd(t)
	path := writeConfig(t, dir, fmt.Sprintf(`name: itest
services:
  - name: one
    cmd: %s
    port: %d
  - name: two
    cmd: %s
    port: %d
`, cmd, one, cmd, two))

	c := startDaemon(t, ctx, dir, []int{one, two})

	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{
		ConfigPath: &path, Only: []string{"two"},
	}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	defer s.Close()

	chunks, end := collect(t, s)
	if len(chunks) != 1 || chunks[0].Service != "two" {
		t.Fatalf("chunks = %+v, want only two", chunks)
	}
	if len(end.Started) != 1 {
		t.Fatalf("end = %+v", end)
	}
	t.Cleanup(func() { killPIDs(chunks) })

	// A name the file does not declare is an error, not a silent no-op.
	_, err = c.Stream(ctx, "groups.start", rpc.GroupsStartParams{
		ConfigPath: &path, Only: []string{"thre"},
	}, nil)
	var re *rpc.Error
	if err == nil {
		t.Fatal("an unknown --only entry should fail")
	}
	if !asRPCError(err, &re) || re.Data.Code != "not_found" {
		t.Fatalf("error = %v, want not_found", err)
	}
}

// TestDaemonAnnouncesStartDependencyCapability: the CLI feature-detects the
// closure field on this capability, so a daemon serving groups.start must
// announce it.
func TestDaemonAnnouncesStartDependencyCapability(t *testing.T) {
	skipOnWindows(t)
	dir := isolate(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := startDaemon(t, ctx, dir, nil)
	if !slices.Contains(c.Hello().Capabilities, rpc.CapabilityStartDependencies) {
		t.Fatalf("capabilities = %v, want %s",
			c.Hello().Capabilities, rpc.CapabilityStartDependencies)
	}
}

// TestStartIncludesDependencyClosure: include_dependencies grows only's plan
// with its transitive depends_on closure, and both the chunks and the summary
// say which named service brought each one in.
func TestStartIncludesDependencyClosure(t *testing.T) {
	skipOnWindows(t)
	dir := isolate(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbPort, apiPort, webPort := freePort(t), freePort(t), freePort(t)
	cmd := serviceCmd(t)
	path := writeConfig(t, dir, fmt.Sprintf(`name: itest
services:
  - name: web
    cmd: %s
    port: %d
    depends_on: [api]
  - name: api
    cmd: %s
    port: %d
    depends_on: [db]
  - name: db
    cmd: %s
    port: %d
`, cmd, webPort, cmd, apiPort, cmd, dbPort))

	c := startDaemon(t, ctx, dir, []int{dbPort, apiPort, webPort})

	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{
		ConfigPath: &path, Only: []string{"web"}, IncludeDependencies: true,
	}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	defer s.Close()

	chunks, end := collect(t, s)
	if len(chunks) != 3 {
		t.Fatalf("chunks = %+v, want db, api and web", chunks)
	}
	wantOrder := []string{"db", "api", "web"}
	for i, want := range wantOrder {
		if chunks[i].Service != want {
			t.Fatalf("order = %+v, want %v", chunks, wantOrder)
		}
	}
	for _, ch := range chunks[:2] {
		if !slices.Equal(ch.IncludedBy, []string{"web"}) {
			t.Errorf("%s IncludedBy = %v, want [web]", ch.Service, ch.IncludedBy)
		}
	}
	if chunks[2].IncludedBy != nil {
		t.Errorf("web IncludedBy = %v, want nil for the named service", chunks[2].IncludedBy)
	}
	if !slices.Equal(end.Included, []string{"api", "db"}) {
		t.Errorf("end.Included = %v, want [api db]", end.Included)
	}
	if len(end.Started) != 3 || len(end.Errors) != 0 {
		t.Fatalf("end = %+v", end)
	}
	t.Cleanup(func() { killPIDs(chunks) })
}

// TestStartWithoutClosureKeepsTheNamedOnly is the restart path: a call that
// does not set include_dependencies still starts exactly what it named.
func TestStartWithoutClosureKeepsTheNamedOnly(t *testing.T) {
	skipOnWindows(t)
	dir := isolate(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	one, two := freePort(t), freePort(t)
	cmd := serviceCmd(t)
	path := writeConfig(t, dir, fmt.Sprintf(`name: itest
services:
  - name: one
    cmd: %s
    port: %d
  - name: two
    cmd: %s
    port: %d
`, cmd, one, cmd, two))

	c := startDaemon(t, ctx, dir, []int{one, two})

	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{
		ConfigPath: &path, Only: []string{"two"},
	}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	defer s.Close()

	chunks, end := collect(t, s)
	if len(chunks) != 1 || chunks[0].Service != "two" {
		t.Fatalf("chunks = %+v, want only two", chunks)
	}
	if chunks[0].IncludedBy != nil {
		t.Errorf("IncludedBy = %v, want none without closure", chunks[0].IncludedBy)
	}
	if len(end.Included) != 0 {
		t.Errorf("end.Included = %v, want none", end.Included)
	}
	t.Cleanup(func() { killPIDs(chunks) })
}

// TestStartNeedsAName rejects a call that names neither a group nor a file.
func TestStartNeedsAName(t *testing.T) {
	skipOnWindows(t)
	dir := isolate(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := startDaemon(t, ctx, dir, nil)
	_, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{}, nil)
	var re *rpc.Error
	if err == nil || !asRPCError(err, &re) || re.Data.Code != "invalid_params" {
		t.Fatalf("error = %v, want invalid_params", err)
	}
}

func asRPCError(err error, out **rpc.Error) bool {
	e, ok := err.(*rpc.Error)
	if ok {
		*out = e
	}
	return ok
}

// exitsImmediately is a command that starts, does nothing and stops, standing
// in for a service that never manages to listen.
func exitsImmediately(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"/usr/bin/true", "/bin/true"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Skip("no true(1) on this system")
	return ""
}

func dialable(port int) bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// killPIDs stops the children a test started, so a failed run does not leave
// listeners behind.
func killPIDs(chunks []rpc.GroupsStartChunk) {
	for _, c := range chunks {
		if c.PID <= 0 {
			continue
		}
		if p, err := os.FindProcess(c.PID); err == nil {
			_ = p.Kill()
		}
	}
}

// TestEnvFileSuppliesEnvironmentBelowTheManifest: a shared file outside the
// checkout fills the service's environment, its values are not reference
// expanded, and PORT plus the service's own `env:` still win.
func TestEnvFileSuppliesEnvironmentBelowTheManifest(t *testing.T) {
	skipOnWindows(t)
	dir := isolate(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	envPath := filepath.Join(filepath.Dir(dir), "shared.env")
	if err := os.WriteFile(envPath, []byte("FROM_FILE=file-value\nCALLER_LOSES=file-value\nOVERRIDDEN=file-value\nPORT=9999\nLITERAL=${not-expanded}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	cmd := serviceCmd(t)
	path := writeConfig(t, dir, fmt.Sprintf(`name: envtest
services:
  - name: api
    cmd: %s
    port: %d
    env_file: ../shared.env
    env:
      BERTH_TEST_ECHO: FROM_FILE,CALLER_LOSES,OVERRIDDEN,PORT,LITERAL
      OVERRIDDEN: from-manifest
`, cmd, port))

	c := startDaemon(t, ctx, dir, []int{port})
	var start rpc.GroupsStartResult
	s, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{
		ConfigPath: &path,
		Env:        map[string]string{"CALLER_LOSES": "caller-value"},
	}, &start)
	if err != nil {
		t.Fatalf("groups.start: %v", err)
	}
	defer s.Close()

	chunks, end := collect(t, s)
	t.Cleanup(func() { killPIDs(chunks) })
	if len(end.Started) != 1 || len(end.Errors) != 0 {
		dumpLogs(t, chunks)
		t.Fatalf("end = %+v, chunks = %+v", end, chunks)
	}

	want := []string{
		"FROM_FILE=file-value",
		"CALLER_LOSES=file-value",
		"OVERRIDDEN=from-manifest",
		fmt.Sprintf("PORT=%d", port),
		"LITERAL=${not-expanded}",
	}
	// The child prints its environment just before it listens; give the log a
	// moment to arrive instead of racing the write.
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, _ := os.ReadFile(chunks[0].LogPath)
		missing := ""
		for _, w := range want {
			if !strings.Contains(string(raw), w) {
				missing = w
				break
			}
		}
		if missing == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("service log is missing %q:\n%s", missing, raw)
		}
		time.Sleep(50 * time.Millisecond)
	}
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
