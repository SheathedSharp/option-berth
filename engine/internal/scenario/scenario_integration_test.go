//go:build integration

// The smoke for R1.1's fixtures: each scenario is provisioned, a real
// `option-berth` binary is built and started as `serve` in an isolated HOME,
// and the scenario is driven with `up`/`status`/`down` to prove it yields a
// deterministic verdict — no text parsing, no model call. Run with
// `go test -tags integration ./internal/scenario/...`.
package scenario

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/internal/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }

// buildBinary compiles the real CLI once per run, into this run's own temp root
// so the leak gate recognises (and claims) a daemon that lives there.
var buildBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp(testenv.Root(), "option-berth-scenario-bin")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "oberth")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = repoRoot()
	if _, err := cmd.CombinedOutput(); err != nil {
		return "", err
	}
	return bin, nil
})

// repoRoot walks up from this file to the module (go.mod) root.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return "."
}

// env is one isolated daemon: its own HOME, its own short socket, its own DB.
type env struct {
	t      *testing.T
	bin    string
	home   string
	socket string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	bin, err := buildBinary()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	sockDir, err := os.MkdirTemp("", "scnr") // macOS caps a unix socket at ~104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	e := &env{t: t, bin: bin, home: home, socket: filepath.Join(sockDir, "d.sock")}
	// Services are real detached processes. A test that stops the daemon on
	// purpose makes `down` unreachable (the run registry lives in the daemon,
	// and autostart is off in this harness), and a test that forgets
	// cleanupBuilt leaks the same way. The socket is unique to this env and
	// every process started through it carries it in its environment, so one
	// sweep at the end of the test catches what cleanup missed.
	t.Cleanup(e.killLeakedServices)
	return e
}

// killLeakedServices kills anything still running with this env's socket in
// its environment. The match is on the full socket path, so it cannot reach
// another run's processes; Windows has no portable way to read another
// process's environment and skips the sweep.
func (e *env) killLeakedServices() {
	if runtime.GOOS == "windows" {
		return
	}
	out, err := exec.Command("ps", "eww", "-axo", "pid=,command=").Output()
	if err != nil {
		return
	}
	needle := "BERTH_SOCKET=" + e.socket
	self := os.Getpid()
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, needle) {
			continue
		}
		line = strings.TrimSpace(line)
		space := strings.IndexByte(line, ' ')
		if space < 0 {
			continue
		}
		pid, err := strconv.Atoi(line[:space])
		if err != nil || pid == self {
			continue
		}
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}
}

// cleanupBuilt stops services before the isolated daemon is torn down. A run
// is deliberately detached from the daemon, so killing only `serve` would
// reparent the fixture's worker to launchd and leave it behind after the test.
func (e *env) cleanupBuilt(b Built) {
	if b.Root != "" {
		_, _ = e.run(b.Root, "down", "--force")
	}
	release(e.t, b)
}

// childEnv is the environment every child of this env inherits: its own HOME,
// its own socket and database, daemon autostart off.
func (e *env) childEnv() []string {
	return append(os.Environ(),
		"HOME="+e.home,
		"USERPROFILE="+e.home,
		"BERTH_SOCKET="+e.socket,
		"XDG_RUNTIME_DIR=",
		"BERTH_DB=",
		testenv.ChildEnv(),
	)
}

// command builds an `oberth` invocation pinned to this env, running in dir (the
// project root when a command is project-scoped).
func (e *env) command(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command(e.bin, args...)
	cmd.Dir = dir
	if cmd.Dir == "" {
		cmd.Dir = "."
	}
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = e.childEnv()
	return cmd
}

// serve starts `oberth serve` in the background and waits for the socket.
func (e *env) serve() {
	e.t.Helper()
	cmd := e.command("", "serve")
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		e.t.Fatalf("starting oberth serve: %v", err)
	}
	e.t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if e.t.Failed() && out.Len() > 0 {
			e.t.Logf("daemon output:\n%s", out.String())
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.WaitForSocket(ctx, e.socket, 10*time.Second); err != nil {
		e.t.Fatalf("daemon never came up: %v", err)
	}
}

// run executes an oberth command and returns its output (a nonzero exit is not
// fatal — a failed `up` answers in its JSON, not its exit code).
func (e *env) run(dir string, args ...string) ([]byte, error) {
	return e.command(dir, args...).CombinedOutput()
}

// runJSON runs a command and decodes its stdout as one JSON value.
func (e *env) runJSON(dir string, out any, args ...string) error {
	e.t.Helper()
	data, err := e.run(dir, args...)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// statusDoc is the slice of the status document the smoke asserts on.
type statusDoc struct {
	Scope struct {
		Name string `json:"name"`
		Root string `json:"root"`
	} `json:"scope"`
	Worktree struct {
		Name     string          `json:"name"`
		Status   string          `json:"status"`
		Services []serviceStatus `json:"services"`
		Machine  []struct {
			Name      string `json:"name"`
			Port      int    `json:"port"`
			Listening bool   `json:"listening"`
		} `json:"machine"`
	} `json:"worktree"`
	Changed *struct {
		At    string `json:"at"`
		Empty bool   `json:"empty"`
	} `json:"changed,omitempty"`
	AttentionPath     string `json:"attention_path,omitempty"`
	AttentionRevision string `json:"attention_revision,omitempty"`
	// Ports are the listeners attributed to this project. A port here that no
	// declared service claims is "running but undeclared".
	Ports []struct {
		Port int `json:"port"`
	} `json:"ports"`
	// Exits are the finished runs' failure evidence: the thing that answers
	// "declared but not running — why" (exit code + last lines).
	Exits []struct {
		Name      string   `json:"name"`
		ExitCode  *int     `json:"exit_code"`
		Reason    string   `json:"reason"`
		LastLines []string `json:"last_lines"`
	} `json:"exits"`
}

type serviceStatus struct {
	Name       string  `json:"name"`
	Port       *int    `json:"port"`
	Running    bool    `json:"running"`
	PortActual *int    `json:"port_actual"`
	LogPath    *string `json:"log_path"`
	LastExit   *struct {
		Code   int    `json:"code"`
		Reason string `json:"reason"`
	} `json:"last_exit"`
}

// waitStatus polls `status --json` until the predicate holds or times out. The
// daemon's scan lags an `up` by one interval, so instead of racing it the smoke
// reads the truth as the daemon publishes it.
func (e *env) waitStatus(dir string, want func(statusDoc) bool) statusDoc {
	e.t.Helper()
	// Long enough to absorb both a project's registration scan and a status
	// change the daemon publishes a scan interval later.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var st statusDoc
		if err := e.runJSON(dir, &st, "status", "--json", "--no-mark"); err == nil && want(st) {
			return st
		}
		time.Sleep(250 * time.Millisecond)
	}
	e.t.Fatalf("status never matched for %s (last poll below)", dir)
	return statusDoc{}
}

// serviceRunning is a predicate helper: the named service is running (and, when
// a port is expected, on exactly that port).
func (st statusDoc) serviceRunning(name string, port int, expectPort bool) bool {
	for _, s := range st.Worktree.Services {
		if s.Name != name {
			continue
		}
		if !s.Running {
			return false
		}
		if expectPort {
			return s.PortActual != nil && *s.PortActual == port
		}
		return s.PortActual == nil
	}
	return false
}

func (st statusDoc) service(name string) (serviceStatus, bool) {
	for _, s := range st.Worktree.Services {
		if s.Name == name {
			return s, true
		}
	}
	return serviceStatus{}, false
}

func namedClean(t *testing.T, got []string, want ...string) bool {
	t.Helper()
	seen := map[string]bool{}
	for _, s := range got {
		seen[s] = true
	}
	for _, w := range want {
		if !seen[w] {
			return false
		}
	}
	return true
}

func TestAPIServerAndWorkerEndToEnd(t *testing.T) {
	e := newEnv(t)
	e.serve()

	// The project lives inside this env's HOME: the daemon refuses to start a
	// command whose project root is outside the user's home (outside_home).
	b, err := APIServerAndWorker(filepath.Join(e.home, "api-worker"))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() {
		e.cleanupBuilt(b)
		_, _ = e.run("", "daemon", "stop", "--json")
	})
	var up struct {
		Started []string `json:"started"`
		Skipped []string `json:"skipped"`
		Errors  []string `json:"errors"`
	}
	if err := e.runJSON(b.Root, &up, "up", "--json"); err != nil {
		t.Fatalf("up: %v", err)
	}
	if len(up.Errors) != 0 {
		t.Fatalf("up reported errors: %v", up.Errors)
	}
	if !namedClean(t, append(up.Started, up.Skipped...), "api", "worker") {
		t.Errorf("up did not account for both services (started=%v skipped=%v)", up.Started, up.Skipped)
	}

	// Both running: api on its port, the no-port worker through its run record.
	st := e.waitStatus(b.Root, func(d statusDoc) bool {
		return d.serviceRunning("api", b.Port, true) && d.serviceRunning("worker", 0, false)
	})
	if st.Scope.Name != "api-worker" {
		t.Errorf("scope name = %q, want api-worker", st.Scope.Name)
	}

	// The worker has no listener, so this is the case a direct port-only scan
	// used to lose. Stop only the collector; the run itself stays alive and the
	// fallback status must still report both services.
	if _, err := e.run("", "daemon", "stop", "--json"); err != nil {
		t.Fatalf("daemon stop: %v", err)
	}
	stdout, _, err := e.runCapture(b.Root, "status", "--json", "--no-mark")
	if err != nil {
		t.Fatalf("direct status after daemon stop: %v", err)
	}
	var direct statusDoc
	if err := json.Unmarshal(stdout, &direct); err != nil {
		t.Fatalf("decode direct status: %v\n%s", err, stdout)
	}
	if !direct.serviceRunning("worker", 0, false) {
		t.Fatalf("direct status lost no-port worker: %+v", direct.Worktree.Services)
	}
	if !direct.serviceRunning("api", b.Port, true) {
		t.Fatalf("direct status lost api: %+v", direct.Worktree.Services)
	}

	if _, err := e.run("", "daemon", "restart", "--json"); err != nil {
		t.Fatalf("daemon restart before cleanup: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.WaitForSocket(ctx, e.socket, 10*time.Second); err != nil {
		t.Fatalf("replacement daemon never came up: %v", err)
	}
	var restarted struct {
		Stopped struct {
			OK      bool              `json:"ok"`
			Results []json.RawMessage `json:"results"`
		} `json:"stopped"`
		Started []string `json:"started"`
		Errors  []string `json:"errors"`
	}
	if err := e.runJSON(b.Root, &restarted, "restart", "--only", "worker", "--json"); err != nil {
		t.Fatalf("restart worker: %v", err)
	}
	if len(restarted.Errors) != 0 || !namedClean(t, restarted.Started, "worker") {
		t.Fatalf("scoped restart did not start worker: %+v", restarted)
	}
	e.waitStatus(b.Root, func(d statusDoc) bool {
		return d.serviceRunning("api", b.Port, true) && d.serviceRunning("worker", 0, false)
	})
	var down struct {
		OK bool `json:"ok"`
	}
	if err := e.runJSON(b.Root, &down, "down", "--json"); err != nil {
		t.Fatalf("down: %v", err)
	}
	if !down.OK {
		t.Error("down reported ok=false")
	}
}

func TestStartupFailureEndToEnd(t *testing.T) {
	e := newEnv(t)
	e.serve()

	b, err := StartupFailure(filepath.Join(e.home, "startup-failure"))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() { e.cleanupBuilt(b) })
	// Starting may succeed at spawn and the services die a moment later — the
	// durable truth is the status aggregate, so assert that, not up's summary.
	_ = e.runJSON(b.Root, &upDoc{}, "up", "--json")

	// The status aggregate shows both declared services and neither running —
	// "declared but not running".
	st := e.waitStatus(b.Root, func(d statusDoc) bool {
		broken, clash := false, false
		for _, s := range d.Worktree.Services {
			switch s.Name {
			case "broken":
				broken = !s.Running
			case "clash":
				clash = !s.Running
			}
		}
		return broken && clash
	})
	if st.Worktree.Name != "startup-failure" {
		t.Errorf("worktree name = %q, want startup-failure", st.Worktree.Name)
	}

	// And the failure evidence is reachable: a finished run for `broken` with a
	// nonzero exit and its last lines. That is the answer to "why isn't it up".
	e.waitExit(b.Root, "broken")
}

// upDoc is just enough of `up --json` for the smoke to discard it.
type upDoc struct {
	Started  []string `json:"started"`
	Skipped  []string `json:"skipped"`
	Errors   []string `json:"errors"`
	Services []struct {
		Service string `json:"service"`
		Error   string `json:"error"`
	} `json:"services"`
}

// waitExit polls `status --json` until the named service has a finished run
// carrying a nonzero exit code and its last lines.
func (e *env) waitExit(dir, name string) {
	e.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var st statusDoc
		if err := e.runJSON(dir, &st, "status", "--json", "--no-mark"); err == nil {
			for _, run := range st.Exits {
				if run.Name == name && run.ExitCode != nil && *run.ExitCode != 0 && len(run.LastLines) > 0 {
					return
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	e.t.Fatalf("no failed run for %q within deadline", name)
}

func TestDualWorktreeEndToEnd(t *testing.T) {
	e := newEnv(t)
	e.serve()

	// Both checkouts live inside this env's HOME (see the outside_home guard);
	// they are siblings, so the linked worktree is not nested in the main one.
	root := filepath.Join(e.home, "dual")
	main, wt, err := DualWorktree(filepath.Join(root, "main"), filepath.Join(root, "wt"))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() {
		e.cleanupBuilt(main)
		e.cleanupBuilt(wt)
		_, _ = e.run("", "daemon", "stop", "--json")
	})
	// The daemon's canonical identity for a linked checkout is derived from
	// the repository and worktree path. Keep the manifest name different here
	// so status must join the same identity that up used.
	manifest, err := os.ReadFile(wt.Manifest)
	if err != nil {
		t.Fatalf("read linked manifest: %v", err)
	}
	manifestText := strings.Replace(string(manifest), "name: dual-main@wt", "name: custom-feature", 1)
	if err := os.WriteFile(wt.Manifest, []byte(manifestText), 0o644); err != nil {
		t.Fatalf("rewrite linked manifest: %v", err)
	}
	// Each checkout reads its own identity: the two projects do not blur into one.
	// `up` first — a project's manifest enters the daemon's index when it is read or
	// started, not on an obsolete machine-wide list; and the point is that two started checkouts
	// each report their own group and their own port, never the other's.
	for _, d := range []string{main.Root, wt.Root} {
		var up struct {
			Errors []string `json:"errors"`
		}
		if err := e.runJSON(d, &up, "up", "--json"); err != nil {
			t.Fatalf("up %s: %v", d, err)
		}
		if len(up.Errors) != 0 {
			t.Fatalf("up %s reported errors: %v", d, up.Errors)
		}
	}
	mainSt := e.waitStatus(main.Root, func(d statusDoc) bool {
		return d.Worktree.Name == "dual-main" && d.serviceRunning("api", main.Port, true)
	})
	wtSt := e.waitStatus(wt.Root, func(d statusDoc) bool {
		return d.Worktree.Name == "dual-main@wt" && d.serviceRunning("api", wt.Port, true)
	})
	if mainSt.Scope.Name != "dual-main" || wtSt.Scope.Name != "dual-main@wt" {
		t.Errorf("scope names = %q / %q, want dual-main / dual-main@wt", mainSt.Scope.Name, wtSt.Scope.Name)
	}
}

// TestDualWorktreeIsolationWorkflow is the V3 acceptance flow. It uses two
// real checkouts of one repository and drives the same commands an agent uses:
// both up, both status, one down, daemon restart, then a linked-worktree move
// and re-discovery. Fixed and auto ports, service names, process liveness and
// log sources are checked at every boundary so a passing fixture alone cannot
// hide cross-worktree state.
func TestDualWorktreeIsolationWorkflow(t *testing.T) {
	e := newEnv(t)
	e.serve()

	root := filepath.Join(e.home, "dual-workflow")
	main, linked, err := DualWorktree(filepath.Join(root, "main"), filepath.Join(root, "wt"))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	// The linked checkout is moved part-way through the flow. Capture the
	// mutable value so cleanup always addresses its final location.
	eMoved := &linked
	t.Cleanup(func() {
		e.cleanupBuilt(main)
		e.cleanupBuilt(*eMoved)
		_, _ = e.run("", "daemon", "stop", "--json")
	})

	start := func(dir string) {
		t.Helper()
		var up upDoc
		if err := e.runJSON(dir, &up, "up", "--json"); err != nil {
			t.Fatalf("up %s: %v", dir, err)
		}
		if len(up.Errors) != 0 {
			t.Fatalf("up %s errors: %v", dir, up.Errors)
		}
	}
	down := func(dir string) {
		t.Helper()
		var result struct {
			OK bool `json:"ok"`
		}
		if err := e.runJSON(dir, &result, "down", "--json"); err != nil {
			t.Fatalf("down %s: %v", dir, err)
		}
		if !result.OK {
			t.Fatalf("down %s returned ok=false", dir)
		}
	}

	// Both worktrees use the same service names. Each has one fixed api port
	// and one daemon-assigned worker port, and all four listeners must differ.
	start(main.Root)
	start(linked.Root)
	mainSt := e.waitStatus(main.Root, func(d statusDoc) bool {
		api, apiOK := d.service("api")
		worker, workerOK := d.service("worker")
		return d.Worktree.Name == "dual-main" && apiOK && workerOK && api.Running && worker.Running &&
			api.PortActual != nil && *api.PortActual == main.Port && worker.Port == nil && worker.PortActual != nil
	})
	linkedSt := e.waitStatus(linked.Root, func(d statusDoc) bool {
		api, apiOK := d.service("api")
		worker, workerOK := d.service("worker")
		return d.Worktree.Name == "dual-main@wt" && apiOK && workerOK && api.Running && worker.Running &&
			api.PortActual != nil && *api.PortActual == linked.Port && worker.Port == nil && worker.PortActual != nil
	})
	mainWorker, _ := mainSt.service("worker")
	linkedWorker, _ := linkedSt.service("worker")
	if *mainWorker.PortActual == *linkedWorker.PortActual || main.Port == linked.Port ||
		main.Port == *mainWorker.PortActual || main.Port == *linkedWorker.PortActual ||
		linked.Port == *mainWorker.PortActual || linked.Port == *linkedWorker.PortActual {
		t.Fatalf("worktree ports overlap: main api=%d worker=%d, linked api=%d worker=%d",
			main.Port, *mainWorker.PortActual, linked.Port, *linkedWorker.PortActual)
	}
	wantMainRoot, _ := filepath.EvalSymlinks(main.Root)
	wantLinkedRoot, _ := filepath.EvalSymlinks(linked.Root)
	if mainSt.Scope.Root != wantMainRoot || linkedSt.Scope.Root != wantLinkedRoot {
		t.Fatalf("status roots = %q / %q, want %q / %q", mainSt.Scope.Root, linkedSt.Scope.Root, wantMainRoot, wantLinkedRoot)
	}

	// Logs are addressed by the canonical worktree group, even though the
	// service names are identical. The source path and line must belong to the
	// requested checkout.
	readLog := func(dir, service, group, want string) {
		t.Helper()
		var logs struct {
			Source string   `json:"source"`
			Lines  []string `json:"lines"`
		}
		if err := e.runJSON(dir, &logs, "logs", service, "--once", "--json"); err != nil {
			t.Fatalf("logs %s/%s: %v", dir, service, err)
		}
		if !strings.Contains(logs.Source, filepath.Join("logs", group, service+".log")) ||
			!strings.Contains(strings.Join(logs.Lines, "\n"), want) {
			t.Fatalf("logs %s/%s = source %q lines %q, want group %q and marker %q", dir, service, logs.Source, logs.Lines, group, want)
		}
	}
	readLog(main.Root, "api", "dual-main", "api listening on "+strconv.Itoa(main.Port))
	readLog(linked.Root, "api", "dual-main@wt", "api listening on "+strconv.Itoa(linked.Port))
	readLog(main.Root, "worker", "dual-main", "auto worker listening")
	readLog(linked.Root, "worker", "dual-main@wt", "auto worker listening")

	// Stopping A must leave B's fixed and auto listeners alive, and all of A's
	// listeners must disappear (including the auto service process).
	down(main.Root)
	if !waitPortClosed(main.Port, 10*time.Second) || !waitPortClosed(*mainWorker.PortActual, 10*time.Second) {
		t.Fatalf("main listeners survived down: api=%d worker=%d", main.Port, *mainWorker.PortActual)
	}
	mainStopped := e.waitStatus(main.Root, func(d statusDoc) bool {
		api, apiOK := d.service("api")
		worker, workerOK := d.service("worker")
		return apiOK && workerOK && !api.Running && !worker.Running
	})
	_ = mainStopped
	if !waitPortOpen(linked.Port, 2*time.Second) || !waitPortOpen(*linkedWorker.PortActual, 2*time.Second) {
		t.Fatalf("linked listeners were affected by main down: api=%d worker=%d", linked.Port, *linkedWorker.PortActual)
	}
	e.waitStatus(linked.Root, func(d statusDoc) bool {
		return d.serviceRunning("api", linked.Port, true) && d.serviceRunning("worker", *linkedWorker.PortActual, true)
	})

	// Restart the collector while B remains up. Its status and logs must still
	// point at B's worktree and run records.
	if _, err := e.run("", "daemon", "restart", "--json"); err != nil {
		t.Fatalf("daemon restart: %v", err)
	}
	if err := client.WaitForSocket(context.Background(), e.socket, 10*time.Second); err != nil {
		t.Fatalf("daemon after restart: %v", err)
	}
	e.waitStatus(linked.Root, func(d statusDoc) bool {
		return d.Worktree.Name == "dual-main@wt" && d.serviceRunning("api", linked.Port, true)
	})
	readLog(linked.Root, "worker", "dual-main@wt", "auto worker listening")

	// Move the linked checkout through git so its worktree metadata is updated,
	// then restart and ask status from the new path. The old path is gone and the
	// canonical identity changes only in the worktree suffix.
	down(linked.Root)
	if !waitPortClosed(linked.Port, 10*time.Second) || !waitPortClosed(*linkedWorker.PortActual, 10*time.Second) {
		t.Fatalf("linked listeners survived down before move")
	}
	movedRoot := filepath.Join(root, "moved")
	if err := git(main.Root, "worktree", "move", linked.Root, movedRoot); err != nil {
		t.Fatalf("git worktree move: %v", err)
	}
	linked.Root = movedRoot
	linked.Manifest = filepath.Join(movedRoot, ConfigName)
	*eMoved = linked
	if _, err := e.run("", "daemon", "restart", "--json"); err != nil {
		t.Fatalf("daemon restart after move: %v", err)
	}
	if err := client.WaitForSocket(context.Background(), e.socket, 10*time.Second); err != nil {
		t.Fatalf("daemon after move restart: %v", err)
	}
	wantMovedRoot, _ := filepath.EvalSymlinks(movedRoot)
	e.waitStatus(movedRoot, func(d statusDoc) bool {
		return d.Scope.Root == wantMovedRoot && d.Worktree.Name == "dual-main@moved"
	})
	start(movedRoot)
	movedSt := e.waitStatus(movedRoot, func(d statusDoc) bool {
		// The API and auto-port worker become observable independently.
		// Wait for both facts that the assertions below require, within the
		// existing deadline; an API-only snapshot need not have seen worker.
		worker, ok := d.service("worker")
		return d.Worktree.Name == "dual-main@moved" && d.serviceRunning("api", linked.Port, true) &&
			ok && worker.Running && worker.PortActual != nil
	})
	movedWorker, ok := movedSt.service("worker")
	if !ok || movedWorker.PortActual == nil || !movedWorker.Running {
		t.Fatalf("moved worker status = %+v", movedWorker)
	}
	readLog(movedRoot, "api", "dual-main@moved", "api listening on "+strconv.Itoa(linked.Port))
	readLog(movedRoot, "worker", "dual-main@moved", "auto worker listening")
}

// TestConcurrentUpIsSerialized: two `oberth up` calls for one group must not
// race their alreadyRunning checks against each other. Held to one at a time,
// the first call starts both services and the second skips both; without the
// group's lifecycle lock the loser of the race tried to bind a port that was
// already coming up and reported a spurious failure (reproduced on 4 of 6
// runs before the lock).
func TestConcurrentUpIsSerialized(t *testing.T) {
	e := newEnv(t)
	e.serve()

	b, err := APIServerAndWorker(filepath.Join(e.home, "concurrent-up"))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() { e.cleanupBuilt(b) })

	var wg sync.WaitGroup
	docs := make([]upDoc, 2)
	errs := make([]error, 2)
	for i := range docs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = e.runJSON(b.Root, &docs[i], "up", "--json")
		}(i)
	}
	wg.Wait()

	started := map[string]int{}
	skipped := map[string]int{}
	for i := range docs {
		if errs[i] != nil {
			t.Fatalf("up %d: %v", i, errs[i])
		}
		if len(docs[i].Errors) != 0 {
			t.Fatalf("up %d reported errors: %v", i, docs[i].Errors)
		}
		for _, name := range docs[i].Started {
			started[name]++
		}
		for _, name := range docs[i].Skipped {
			skipped[name]++
		}
	}
	if started["api"] != 1 || started["worker"] != 1 {
		t.Fatalf("started counts = %v, want exactly one start each", started)
	}
	if skipped["api"] != 1 || skipped["worker"] != 1 {
		t.Fatalf("skipped counts = %v, want the second call to skip both", skipped)
	}

	e.waitStatus(b.Root, func(d statusDoc) bool {
		api, apiOK := d.service("api")
		worker, workerOK := d.service("worker")
		return apiOK && workerOK && api.Running && worker.Running
	})
}
