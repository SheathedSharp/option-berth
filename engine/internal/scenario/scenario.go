// Package scenario provisions the reusable "declaration vs reality" test
// targets R1.3 and R2.5 build on. Each scenario is a self-contained project
// (a git checkout holding an oberth.yaml and the service code it declares),
// built with its own ports so it can be provisioned twice without colliding.
//
// This package never touches the machine's real install. The fixtures are
// meant to be driven through a real `oberth` binary with an isolated HOME and
// socket (see the integration test for the envelope), so a failing or a
// leaked service can only take the throwaway environment down with it.
package scenario

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ConfigName is the file a project commits at its root to name itself and
// declare its services. Repeated here (rather than importing internal/groups)
// so the fixture stays a leaf that nothing in the engine depends on.
const ConfigName = "oberth.yaml"

// Built is one provisioned scenario: a self-contained project a test can point
// a real `oberth` at. Cleanup releases anything Build still holds (the port a
// scenario occupies so `up` sees it taken); it may be nil.
type Built struct {
	Root      string // repo root (a git checkout)
	Manifest  string // absolute path of oberth.yaml
	Group     string // the name `status` will report for the project
	Port      int    // the port the primary service binds (0 when none)
	ExtraPort int    // an additional fixture listener, when a scenario has one
	Cleanup   func() // release a held listener; may be nil
}

// freePort asks the OS for a currently-unused listening port and releases it.
// The caller binds it next; the small race is acceptable for a fixture.
func freePort() int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// holdPort binds port and keeps accepting until the returned closer runs. The
// daemon's port scan sees the listener, and a service that tries to bind the
// same port crashes with "address already in use".
func holdPort(port int) (closer func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return nil, err
	}
	go func() { // accept until Close ends the pending Accept with an error
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return func() { _ = ln.Close() }, nil
}

// git runs one git command pinning the working tree to root.
func git(root string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// commitAll adds and commits everything in a repo, with a fixture identity so
// the commit does not depend on the developer's git config.
func commitAll(root string) error {
	if err := git(root, "add", "-A"); err != nil {
		return err
	}
	return git(root, "-c", "user.name=scenario", "-c", "user.email=scenario@test",
		"commit", "-m", "scenario fixture")
}

// writeTree writes the given files under dir, creating parents.
func writeTree(dir string, files map[string]string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// project writes the files of one checkout, git-init and committing them. It
// returns the Built it produces.
func project(group string, port int, dir string, files map[string]string) (Built, error) {
	if err := writeTree(dir, files); err != nil {
		return Built{}, err
	}
	if err := git(dir, "init", "-q"); err != nil {
		return Built{}, err
	}
	if err := commitAll(dir); err != nil {
		return Built{}, err
	}
	return Built{Root: dir, Manifest: filepath.Join(dir, ConfigName), Group: group, Port: port}, nil
}

// writeNote drops a one-line "what it declares -> what `status` shows" note, so
// a fixture never looks finished without saying what it is for.
func writeNote(dir, body string) error {
	return os.WriteFile(filepath.Join(dir, "README.scenario.md"), []byte(body), 0o644)
}

// py fills a python template's __PORT__ placeholders.
func py(tmpl string, port int) string {
	return strings.ReplaceAll(tmpl, "__PORT__", strconv.Itoa(port))
}

// apiServerPy binds a single port and stays up. The port is baked in at build
// time: the fixture chose it so a second provision does not collide.
const apiServerPy = `import socket, time
ln = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
ln.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
ln.bind(("127.0.0.1", __PORT__))
ln.listen(5)
print("api listening on __PORT__", flush=True)
while True:
    time.sleep(3600)
`

// autoServerPy binds the PORT supplied by the daemon for a `port: auto`
// service. Keeping the listener in the fixture makes the assigned port visible
// to the real scanner and gives the workflow an independent log to compare.
const autoServerPy = `import os, socket, time
port = int(os.environ["PORT"])
ln = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
ln.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
ln.bind(("127.0.0.1", port))
ln.listen(5)
print("auto worker listening on %d" % port, flush=True)
while True:
    time.sleep(3600)
`

// workerPy runs but binds nothing: the no-port half of the "declared and really
// running" case — the daemon has to see it through its run registry, not a port.
const workerPy = `import time
print("worker running", flush=True)
while True:
    time.sleep(3600)
`

// APIServerAndWorker provisions the port + no-port project: `api` binds a port
// and `worker` runs with none. It is the fixture R1.3's "declared and running"
// case and the liveness path are asserted against.
func APIServerAndWorker(dir string) (Built, error) {
	port := freePort()
	files := map[string]string{
		ConfigName: fmt.Sprintf(`name: api-worker
services:
  - name: worker
    cmd: python3 worker.py
  - name: api
    cmd: python3 api.py
    port: %d
    depends_on: [worker]
`, port),
		"api.py":    py(apiServerPy, port),
		"worker.py": workerPy,
	}
	b, err := project("api-worker", port, dir, files)
	if err != nil {
		return Built{}, err
	}
	if err := writeNote(dir, "# api-worker\n\ndeclares `api` (port "+strconv.Itoa(port)+") + `worker` (no port);\nwhen both run, `oberth status` shows them both — the worker through its run record, not a port.\n"); err != nil {
		return Built{}, err
	}
	return b, nil
}

// crashPy tries to bind a port the scenario already holds, and dies with the
// OS's "address already in use".
const crashPy = `import socket, sys
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
try:
    s.bind(("127.0.0.1", __PORT__))
except OSError as e:
    print("clash: could not bind: %s" % e, file=sys.stderr, flush=True)
    sys.exit(1)
s.listen(5)
import time
while True:
    time.sleep(3600)
`

const brokenSh = `echo "broken: this service is not going to start" >&2
exit 1
`

// StartupFailure provisions a project whose declared services cannot come up:
// `broken` exits immediately and `clash` runs but loses the bind to a port the
// fixture is holding. The project is the "declared but not running" case, with
// the failure evidence (exit code, last lines) a reader should be able to reach.
func StartupFailure(dir string) (Built, error) {
	port := freePort()
	release, err := holdPort(port)
	if err != nil {
		return Built{}, fmt.Errorf("holding port %d: %w", port, err)
	}
	files := map[string]string{
		ConfigName: fmt.Sprintf(`name: startup-failure
services:
  - name: broken
    cmd: sh broken.sh
  - name: clash
    cmd: python3 clash.py
    port: %d
`, port),
		"broken.sh": brokenSh,
		"clash.py":  py(crashPy, port),
	}
	b, err := project("startup-failure", port, dir, files)
	if err != nil {
		release()
		return Built{}, err
	}
	b.Cleanup = release
	if err := writeNote(dir, "# startup-failure\n\ndeclares `broken` (exits 1) and `clash` (port "+strconv.Itoa(port)+" is held by the fixture);\nneither runs, and `oberth up` / `status` report the failure with the exit record.\n"); err != nil {
		release()
		return Built{}, err
	}
	return b, nil
}

// RecoveryService provisions one long-lived service whose output makes daemon
// restart and logs assertions unambiguous. The service binds the returned port
// and prints a marker before accepting connections.
func RecoveryService(dir string) (Built, error) {
	port := freePort()
	files := map[string]string{
		ConfigName: fmt.Sprintf(`name: recovery
services:
  - name: api
    cmd: python3 api.py
    port: %d
`, port),
		"api.py": py(`import socket, time
print("recovery service ready", flush=True)
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", __PORT__))
s.listen(5)
while True:
    c, _ = s.accept()
    c.close()
`, port),
	}
	b, err := project("recovery", port, dir, files)
	if err != nil {
		return Built{}, err
	}
	if err := writeNote(dir, "# recovery\n\nlong-lived api used to verify daemon stop/restart, logs, and down cleanup.\n"); err != nil {
		return Built{}, err
	}
	return b, nil
}

// FailureMatrix provisions three independent service failures in one project:
// a command that cannot be executed, a process that exits after writing a
// useful log line, and a fixed port held by another process. The returned
// cleanup releases the competing listener after the assertions complete.
func FailureMatrix(dir string) (Built, error) {
	port := freePort()
	release, err := holdPort(port)
	if err != nil {
		return Built{}, fmt.Errorf("holding port %d: %w", port, err)
	}
	files := map[string]string{
		ConfigName: fmt.Sprintf(`name: failure-matrix
services:
  - name: missing
    cmd: option-berth-command-that-does-not-exist
  - name: idle
    cmd: python3 idle.py
  - name: exited
    cmd: sh -c 'echo exited with code 7; exit 7'
  - name: occupied
    cmd: python3 occupied.py
    port: %d
`, port),
		"idle.py":     workerPy,
		"occupied.py": py(crashPy, port),
	}
	b, err := project("failure-matrix", 0, dir, files)
	if err != nil {
		release()
		return Built{}, err
	}
	b.Port = port
	b.Cleanup = release
	if err := writeNote(dir, "# failure-matrix\n\nmissing command, immediate exit, and a fixed port held by another process.\n"); err != nil {
		release()
		return Built{}, err
	}
	return b, nil
}

// ForceTree provisions a service that ignores SIGTERM and owns a child
// listener. It is the real process-tree fixture for the recovery matrix:
// normal down must escalate to SIGKILL and remove both listeners.
func ForceTree(dir string) (Built, error) {
	parent, child := freePort(), freePort()
	files := map[string]string{
		ConfigName: fmt.Sprintf(`name: force-tree
services:
  - name: supervisor
    cmd: python3 tree.py
    port: %d
`, parent),
		"tree.py": fmt.Sprintf(`import os, signal, socket, subprocess, sys, time
signal.signal(signal.SIGTERM, signal.SIG_IGN)
child = subprocess.Popen([sys.executable, "-c", %q])
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", %d))
s.listen(5)
print("tree parent ready", flush=True)
while True:
    c, _ = s.accept()
    c.close()
`, fmt.Sprintf("import socket,time\ns=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('127.0.0.1',%d));s.listen(5);print('tree child ready',flush=True)\nwhile True:\n c,_=s.accept()\n c.close()", child), parent),
	}
	b, err := project("force-tree", parent, dir, files)
	if err != nil {
		return Built{}, err
	}
	if err := writeNote(dir, fmt.Sprintf("# force-tree\n\n`supervisor` owns listeners %d and %d and ignores SIGTERM; down must clean the whole tree.\n", parent, child)); err != nil {
		return Built{}, err
	}
	// The child port is intentionally not declared; callers use it to prove
	// tree cleanup rather than service matching.
	b.ExtraPort = child
	return b, nil
}

// MachineDependency provisions a project with a long-running `worker` service
// and a machine-level dependency (the top-level `machine:` section) on a port
// the fixture does NOT occupy. The worker exists so `up` has something to start
// and thereby register the project in the daemon's index (a service-less config
// is never discovered on its own) — a project that leans on a machine dependency
// usually runs its own services too. The `machine:` ref is a reference
// (0001/0010): it carries no cmd and `up`/`down` never touch it; it only says
// whether the thing is there.
func MachineDependency(dir, depName string) (Built, error) {
	port := freePort()
	b, err := project("machine-dep", 0, dir, map[string]string{
		ConfigName: fmt.Sprintf(`name: machine-dep
services:
  - name: worker
    cmd: python3 worker.py
machine:
  - name: %s
    port: %d
`, depName, port),
		"worker.py": workerPy,
	})
	if err != nil {
		return Built{}, err
	}
	if err := writeNote(dir, "# machine-dep\n\nruns a `worker` and declares `machine:` dependency "+depName+" (port "+strconv.Itoa(port)+");\n`oberth status` shows the dependency `listening: false` until that port is held.\n"); err != nil {
		return Built{}, err
	}
	return b, nil
}

// DualWorktree provisions one repository with two linked checkouts, each
// carrying its own manifest that binds its own port — the fixture for "same
// repo, two worktrees don't cross-attribute" (success criterion 3, R2.5).
// mainRoot and linkedRoot are the directories the two checkouts live in; the
// linked checkout is a real `git worktree`, so `oberth git` sees both.
func DualWorktree(mainRoot, linkedRoot string) (Built, Built, error) {
	mainPort := freePort()
	main, err := project("dual-main", mainPort, mainRoot, map[string]string{
		ConfigName: fmt.Sprintf(`name: dual-main
services:
  - name: api
    cmd: python3 api.py
    port: %d
  - name: worker
    cmd: python3 worker.py
    port: auto
`, mainPort),
		"api.py":    py(apiServerPy, mainPort),
		"worker.py": autoServerPy,
	})
	if err != nil {
		return Built{}, Built{}, err
	}

	// A linked worktree is already a checkout of the same repo; writing new
	// files into it makes it a second project directory without re-initialising
	// git. It stays detached at HEAD: the point is two checkouts, not a commit.
	if err := git(mainRoot, "worktree", "add", "--detach", "-q", linkedRoot, "HEAD"); err != nil {
		return Built{}, Built{}, err
	}
	wtPort := freePort()
	if err := writeTree(linkedRoot, map[string]string{
		ConfigName: fmt.Sprintf(`name: dual-main@wt
services:
  - name: api
    cmd: python3 api.py
    port: %d
  - name: worker
    cmd: python3 worker.py
    port: auto
`, wtPort),
		"api.py":    py(apiServerPy, wtPort),
		"worker.py": autoServerPy,
	}); err != nil {
		return Built{}, Built{}, err
	}
	wt := Built{Root: linkedRoot, Manifest: filepath.Join(linkedRoot, ConfigName), Group: "dual-main@wt", Port: wtPort}

	if err := writeNote(mainRoot, "# dual-worktree\n\nmain checkout (`dual-main`, fixed api port "+strconv.Itoa(mainPort)+") and a linked worktree\n(`dual-main@wt`, fixed api port "+strconv.Itoa(wtPort)+"); both also run the same `worker` service on a daemon-assigned port.\n"); err != nil {
		return Built{}, Built{}, err
	}
	return main, wt, nil
}
