package cmd

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/killer"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/spf13/cobra"
)

// TestKillHelperListener is not a test: re-executing the test binary with
// BERTH_TEST_LISTEN set is how these tests get a real, killable process holding
// a real port.
func TestKillHelperListener(t *testing.T) {
	port := os.Getenv("BERTH_TEST_LISTEN")
	if port == "" {
		t.Skip("helper process, driven by TestKillRoutesThroughTheDaemon")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("helper could not listen: %v", err)
	}
	defer ln.Close()
	time.Sleep(60 * time.Second)
}

// startListener spawns the helper on a free port and returns the port. The
// process is killed on cleanup whether or not the test killed it itself.
func startListener(t *testing.T) int {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the kill routing harness is unix-only")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	port, _ := strconv.Atoi(portStr)

	cmd := exec.Command(os.Args[0], "-test.run=TestKillHelperListener", "-test.timeout=90s")
	cmd.Env = append(os.Environ(), "BERTH_TEST_LISTEN="+portStr)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+portStr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return port
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the helper never listened on %d", port)
	return 0
}

// startScanningDaemon runs a daemon over a real socket with the production
// scanner, so it sees the same processes the direct path scans.
func startScanningDaemon(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket harness does not apply to named pipes")
	}
	dir, err := os.MkdirTemp("", "sn")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")

	srv := daemon.New(daemon.Options{
		Socket:  socket,
		Version: "test",
		Scanner: scanner.New(scanner.Options{DaemonVersion: "test"}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-srv.Done()
	})
	if err := client.WaitForSocket(ctx, socket, 5*time.Second); err != nil {
		t.Fatalf("daemon did not come up: %v", err)
	}

	prev := dialDaemon
	dialDaemon = func(ctx context.Context) (*client.Client, error) {
		return client.Dial(ctx, client.ClientInfo{Name: "cli", Version: "test", Socket: socket})
	}
	t.Cleanup(func() { dialDaemon = prev })
}

// captureStdout collects what fn prints, which is where every kill report goes.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	os.Stdout = prev
	w.Close()
	out := <-done
	r.Close()
	return out
}

// setKillFlags installs one flag combination for the duration of a test.
func setKillFlags(t *testing.T, dryRun, asJSON bool) {
	t.Helper()
	prevDry, prevJSON, prevYes := killDryRunFlag, killJSONFlag, killYesFlag
	killDryRunFlag, killJSONFlag, killYesFlag = dryRun, asJSON, true
	t.Cleanup(func() {
		killDryRunFlag, killJSONFlag, killYesFlag = prevDry, prevJSON, prevYes
	})
}

// runKillCmd drives the real command, so the routing decision itself — daemon
// when one is reachable, direct scan otherwise — is what the test exercises.
func runKillCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := &cobra.Command{RunE: runKill}
	cmd.Flags().String("ip", "", "")
	cmd.SetContext(context.Background())
	var err error
	out := captureStdout(t, func() { err = runKill(cmd, args) })
	return out, err
}

// TestKillJSONIsIdenticalOnBothPaths is the golden for the third
// follow-up: routing `oberth kill` through the daemon must not change a single
// byte of what the user sees. A dry run is what makes the comparison possible —
// the same process is planned twice, so pid, name and method are identical and
// the two JSON documents have to match exactly.
func TestKillJSONIsIdenticalOnBothPaths(t *testing.T) {
	resetRouting(t)
	setKillFlags(t, true, true)
	port := startListener(t)
	arg := []string{strconv.Itoa(port)}

	noDaemonReachable(t)
	direct, err := runKillCmd(t, arg...)
	if err != nil {
		t.Fatalf("the direct path failed: %v", err)
	}
	if !bytes.Contains([]byte(direct), []byte(`"port": `+arg[0])) {
		t.Fatalf("the direct path did not find the listener:\n%s", direct)
	}

	resetRouting(t)
	startScanningDaemon(t)
	routed, err := runKillCmd(t, arg...)
	if err != nil {
		t.Fatalf("the daemon path failed: %v", err)
	}

	if routed != direct {
		t.Fatalf("--json differs between the two paths:\n daemon:\n%s\n direct:\n%s", routed, direct)
	}
}

// TestKillRoutesThroughTheDaemon: with a daemon reachable the kill happens in
// the daemon, which rescans straight afterwards — so the port is gone from the
// daemon's own port table immediately, instead of lingering in its cache for up
// to CacheTTL as it did when the CLI killed directly.
func TestKillRoutesThroughTheDaemon(t *testing.T) {
	resetRouting(t)
	setKillFlags(t, false, true)
	port := startListener(t)
	startScanningDaemon(t)

	out, err := runKillCmd(t, strconv.Itoa(port))
	if err != nil {
		t.Fatalf("kill through the daemon: %v\n%s", err, out)
	}
	if !bytes.Contains([]byte(out), []byte(`"ok": true`)) {
		t.Fatalf("nothing was stopped:\n%s", out)
	}

	ctx := context.Background()
	c, err := dialDaemon(ctx)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer c.Close()

	// The daemon's own view is already up to date: no sleeping out the cache.
	rows, err := daemonList(ctx, c)
	if err != nil {
		t.Fatalf("state.snapshot: %v", err)
	}
	for _, r := range rows {
		if r.Port == port {
			t.Fatalf("port %d is still in the daemon's table right after the kill", port)
		}
	}
}

// TestKillSelectorsCarryExactlyOneSubject pins the wire shape: a selector names
// either a run, a pid or a port+bind, never a mixture. The daemon resolves that
// one field; a selector carrying two would make which one wins an accident.
func TestKillSelectorsCarryExactlyOneSubject(t *testing.T) {
	got := killSelectors([]killer.Target{
		{RunID: "run-1", PID: 42, Port: 3000},
		{PID: 43, Port: 3001},
		{Port: 3002, BindAddress: "127.0.0.1"},
		{Port: 3003},
	})
	if len(got) != 4 {
		t.Fatalf("selectors = %+v, want one per target", got)
	}
	if got[0].RunID == nil || *got[0].RunID != "run-1" || got[0].PID != nil || got[0].Port != nil {
		t.Errorf("a run target must become a run selector only: %+v", got[0])
	}
	if got[1].PID == nil || *got[1].PID != 43 || got[1].Port != nil || got[1].RunID != nil {
		t.Errorf("a pid target must become a pid selector only: %+v", got[1])
	}
	if got[2].Port == nil || *got[2].Port != 3002 || got[2].BindAddress == nil || *got[2].BindAddress != "127.0.0.1" {
		t.Errorf("a port target keeps its bind address: %+v", got[2])
	}
	if got[3].Port == nil || *got[3].Port != 3003 || got[3].BindAddress != nil {
		t.Errorf("no bind address means no bind field: %+v", got[3])
	}
}

// TestGraceMsKeepsTheDaemonDefault: zero is the wire spelling for "use the
// daemon's default grace", and a negative duration must not become a huge
// unsigned millisecond count.
func TestGraceMsKeepsTheDaemonDefault(t *testing.T) {
	for _, tc := range []struct {
		grace time.Duration
		want  int
	}{
		{0, 0},
		{-time.Second, 0},
		{1500 * time.Millisecond, 1500},
	} {
		if got := graceMs(killer.Options{Grace: tc.grace}); got != tc.want {
			t.Errorf("graceMs(%s) = %d, want %d", tc.grace, got, tc.want)
		}
	}
}

// TestKillMethodFollowsTheSelection: only a group selection the daemon can
// resolve by name uses groups.kill. The tree and no-escalate options have no
// groups.kill field, so those rounds must fall back to the already-resolved
// port targets.
func TestKillMethodFollowsTheSelection(t *testing.T) {
	noEscalate := false
	for _, tc := range []struct {
		name  string
		group string
		opts  killer.Options
		want  string
	}{
		{"a port selection", "", killer.Options{}, "ports.kill"},
		{"a group the daemon can resolve", "storefront", killer.Options{}, "groups.kill"},
		{"a group that needs the tree flag", "storefront", killer.Options{Tree: true}, "ports.kill"},
		{"a group that must not escalate", "storefront", killer.Options{Escalate: &noEscalate}, "ports.kill"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := killMethodFor(tc.group, tc.opts); got != tc.want {
				t.Errorf("killMethodFor(%q) = %q, want %q", tc.group, got, tc.want)
			}
		})
	}
}
