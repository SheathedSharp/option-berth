//go:build integration

// R1.3: the "declaration vs reality" cases, pinned to stable JSON and exit-code
// behaviour through a real `oberth`. Every assertion reads the confirmed shape
// of `status --json` — nothing parsed by hand, nothing asked of a model.
package scenario

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/groups"
)

// machineRefPort reads the machine-ref port a manifest declares.
func machineRefPort(t *testing.T, manifest, name string) int {
	t.Helper()
	cfg, err := groups.Load(manifest)
	if err != nil {
		t.Fatalf("loading %s: %v", manifest, err)
	}
	for _, m := range cfg.Machine {
		if m.Name == name {
			return m.Port
		}
	}
	t.Fatalf("manifest %s has no machine ref named %q", manifest, name)
	return 0
}

// machineListening reports whether the group's machine section has an entry
// named name, and whether it is listening.
func machineListening(d statusDoc, name string) (found, listening bool) {
	for _, m := range d.Worktree.Machine {
		if m.Name == name {
			return true, m.Listening
		}
	}
	return false, false
}

// waitMachine polls until the machine ref reports the wanted listening state.
func (e *env) waitMachine(dir, name string, want bool) {
	e.t.Helper()
	e.waitStatus(dir, func(d statusDoc) bool {
		found, listening := machineListening(d, name)
		return found && listening == want
	})
}

// portsContain reports whether the project's listening ports include port.
func portsContain(d statusDoc, port int) bool {
	for _, p := range d.Ports {
		if p.Port == port {
			return true
		}
	}
	return false
}

// declaredPorts is the set of ports the manifest's services claim.
func declaredPorts(d statusDoc) map[int]bool {
	m := map[int]bool{}
	for _, s := range d.Worktree.Services {
		if s.Port != nil {
			m[*s.Port] = true
		}
	}
	return m
}

// waitPortOpen polls until something is listening on port.
func waitPortOpen(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
		if err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

// waitPortClosed is the inverse probe used by lifecycle acceptance tests. A
// successful dial means the service (or an orphaned child) is still holding the
// listener, so down is not considered complete until every owned port closes.
func waitPortClosed(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond)
		if errors.Is(err, syscall.ECONNREFUSED) {
			return true
		}
		// A timeout or routing failure is unknown, not proof of closure.
		if conn != nil {
			_ = conn.Close()
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

// startUndeclaredListener runs a python service inside the project that binds
// port but is NOT declared anywhere — the "running but undeclared" fixture. It
// lives in the project's working directory, so the daemon attributes it to the
// project even though no manifest entry claims the port.
func startUndeclaredListener(t *testing.T, e *env, dir string, port int) *exec.Cmd {
	t.Helper()
	script := strings.ReplaceAll(`import socket, time
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", PORT))
s.listen(5)
print("undeclared up", flush=True)
while True:
    time.sleep(3600)
`, "PORT", strconv.Itoa(port))
	if err := os.WriteFile(filepath.Join(dir, "extra.py"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "extra.py")
	cmd.Dir = dir
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = e.childEnv()
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the undeclared listener: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if !waitPortOpen(port, 10*time.Second) {
		t.Fatalf("the undeclared listener never bound %d", port)
	}
	return cmd
}

// TestManifestCreatedAfterScanIsDiscovered pins decision 0042 end to end: a
// directory the daemon's scan already looked at must not keep "no manifest"
// as a permanent answer. The checkout has no manifest when its listener is
// first attributed (the group exists by git-root name only); the manifest
// written afterwards takes the same name as the checkout, so the daemon's
// snapshot already holds that group and nothing but a real re-probe can
// bring the services in. No restart, no `up`, no command naming the file.
func TestManifestCreatedAfterScanIsDiscovered(t *testing.T) {
	e := newEnv(t)
	e.serve()

	dir := filepath.Join(e.home, "late-manifest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := git(dir, "init", "-q"); err != nil {
		t.Fatalf("git init: %v", err)
	}

	port := freePort()
	startUndeclaredListener(t, e, dir, port)

	// The first status makes the daemon scan: the port is attributed to the
	// git-root group and the directory is now a probed, manifest-less one.
	st := e.waitStatus(dir, func(d statusDoc) bool { return portsContain(d, port) })
	if st.Worktree.Name != "late-manifest" {
		t.Fatalf("group before the manifest = %q, want late-manifest", st.Worktree.Name)
	}
	if len(st.Worktree.Services) != 0 {
		t.Fatalf("services before the manifest = %+v, want none", st.Worktree.Services)
	}

	manifest := "name: late-manifest\nservices:\n  - name: worker\n    cmd: python3 worker.py\n"
	if err := os.WriteFile(filepath.Join(dir, ConfigName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	e.waitStatus(dir, func(d statusDoc) bool {
		for _, s := range d.Worktree.Services {
			if s.Name == "worker" {
				return true
			}
		}
		return false
	})
}

// TestMachineDependencyDownAndUp pins product.md's "清单引用机器级依赖 — 机器上
// 没有对应监听 — 依赖当前不可用": a `machine:` ref shows the listening state the
// scan observes, and the same fixture flips when the port appears. It also
// pins the other half of "机器级服务只读：up / down 不动它们": `down` stops the
// project's own worker and must leave the referenced listener alone.
func TestMachineDependencyDownAndUp(t *testing.T) {
	e := newEnv(t)
	e.serve()

	b, err := MachineDependency(filepath.Join(e.home, "machine-dep"), "mysql")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() { e.cleanupBuilt(b) })
	refPort := machineRefPort(t, b.Manifest, "mysql")
	// The worker's `up` registers the project — a service-less config is never
	// discovered on its own — and leaves the group in the daemon's snapshot.
	var up struct {
		Errors []string `json:"errors"`
	}
	if err := e.runJSON(b.Root, &up, "up", "--json"); err != nil {
		t.Fatalf("up: %v", err)
	}
	if len(up.Errors) != 0 {
		t.Fatalf("up reported errors: %v", up.Errors)
	}

	// Down: the port no one holds is reported as the dependency being unavailable.
	e.waitMachine(b.Root, "mysql", false)

	// Up: hold the ref port; the same declaration now reports it listening.
	releasePort, err := holdPort(refPort)
	if err != nil {
		t.Fatalf("holding the machine ref port %d: %v", refPort, err)
	}
	t.Cleanup(releasePort)
	e.waitMachine(b.Root, "mysql", true)

	// A machine dependency is a reference, not a service of this project:
	// `down` releases the project's own worker but never the referenced port,
	// even when the reference is what kept the port in the group's story.
	var down struct {
		OK bool `json:"ok"`
	}
	if err := e.runJSON(b.Root, &down, "down", "--json"); err != nil {
		t.Fatalf("down: %v", err)
	}
	if !down.OK {
		t.Fatalf("down returned ok=false")
	}
	if !waitPortOpen(refPort, 2*time.Second) {
		t.Fatalf("down stopped the machine dependency on port %d", refPort)
	}
	e.waitMachine(b.Root, "mysql", true)
}

// TestPathRecovery is the R1.4 reproduction: a project whose directory is
// renamed must still be readable from the new path. It starts by up-registering
// under one name, then renames the checkout and reads status from the new cwd.
func TestPathRecovery(t *testing.T) {
	e := newEnv(t)
	e.serve()

	oldDir := filepath.Join(e.home, "api-worker")
	b, err := APIServerAndWorker(oldDir)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() { e.cleanupBuilt(b) })
	var up struct {
		Errors []string `json:"errors"`
	}
	if err := e.runJSON(oldDir, &up, "up", "--json"); err != nil {
		t.Fatalf("up: %v", err)
	}
	if len(up.Errors) != 0 {
		t.Fatalf("up reported errors: %v", up.Errors)
	}
	// Stop the services so nothing keeps the old directory anchored in the
	// daemon's index (a running process with cwd inside it would re-attribute).
	var down struct {
		OK bool `json:"ok"`
	}
	if err := e.runJSON(oldDir, &down, "down", "--json"); err != nil {
		t.Fatalf("down: %v", err)
	}

	// Rename the whole project directory.
	newDir := filepath.Join(e.home, "api-worker-moved")
	if err := os.Rename(oldDir, newDir); err != nil {
		t.Fatalf("renaming the project: %v", err)
	}

	// A daemon restart re-seeds the root index from the DB and prunes the gone
	// old path; the new path was never recorded. This is where the index is lost.
	if _, err := e.run("", "daemon", "stop"); err != nil {
		t.Fatalf("daemon stop: %v", err)
	}
	for i := 0; i < 200; i++ { // wait for the socket to free before serving again
		if _, err := os.Stat(e.socket); err != nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	e.serve()

	// status from the new cwd must still see the project's declaration, not
	// not_found, after the rename + restart.
	e.waitStatus(newDir, func(d statusDoc) bool {
		return d.Worktree.Name == "api-worker"
	})
}

// TestRunningButUndeclared pins product.md's "进程正在监听端口 — 不属于任何清单" :
// a listener inside the project that no manifest service declares shows up as a
// running port, but no declared service claims it.
func TestRunningButUndeclared(t *testing.T) {
	e := newEnv(t)
	e.serve()

	b, err := APIServerAndWorker(filepath.Join(e.home, "run-undecl"))
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() { e.cleanupBuilt(b) })
	var up struct {
		Errors []string `json:"errors"`
	}
	if err := e.runJSON(b.Root, &up, "up", "--json"); err != nil {
		t.Fatalf("up: %v", err)
	}
	if len(up.Errors) != 0 {
		t.Fatalf("up reported errors: %v", up.Errors)
	}

	// An undeclared listener comes up inside the project.
	extraPort := freePort()
	startUndeclaredListener(t, e, b.Root, extraPort)

	st := e.waitStatus(b.Root, func(d statusDoc) bool { return portsContain(d, extraPort) })
	if !portsContain(st, extraPort) {
		t.Fatalf("extra port %d missing from the project's running ports", extraPort)
	}
	if declared := declaredPorts(st); declared[extraPort] {
		t.Errorf("no declared service claims %d, but the status marks one (declared=%v)", extraPort, declared)
	}
}
