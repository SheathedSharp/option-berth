package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/killer"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/sessions"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/spf13/cobra"
)

func killResults() []killer.Result {
	const host = state.LocalhostName
	return []killer.Result{
		{Host: host, Port: 3100, BindAddress: "127.0.0.1", PID: 410, Name: "bundler", Method: state.MethodSIGTERM, OK: true},
		{Host: host, Port: 3100, BindAddress: "127.0.0.1", PID: 310, Name: "devserver", Method: state.MethodSIGKILL, OK: true},
		{Host: host, Port: 5433, BindAddress: "0.0.0.0", PID: 0, Name: "postgres", Method: state.MethodDockerStop, OK: true},
		{Host: host, Port: 8081, BindAddress: "127.0.0.1", PID: 910, Name: "proxy", Method: state.MethodNone, OK: false,
			Error: "not permitted to signal PID 910"},
	}
}

// The JSON shape is the cross-spec contract's kill result list; other repos are
// built against it, so it is pinned here character for character.
func TestKillJSONGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := writeKillJSON(&buf, killResults()); err != nil {
		t.Fatal(err)
	}
	const want = `[
  {
    "host": "localhost",
    "port": 3100,
    "bind_address": "127.0.0.1",
    "pid": 410,
    "name": "bundler",
    "method": "sigterm",
    "ok": true
  },
  {
    "host": "localhost",
    "port": 3100,
    "bind_address": "127.0.0.1",
    "pid": 310,
    "name": "devserver",
    "method": "sigkill",
    "ok": true
  },
  {
    "host": "localhost",
    "port": 5433,
    "bind_address": "0.0.0.0",
    "pid": 0,
    "name": "postgres",
    "method": "docker_stop",
    "ok": true
  },
  {
    "host": "localhost",
    "port": 8081,
    "bind_address": "127.0.0.1",
    "pid": 910,
    "name": "proxy",
    "method": "none",
    "ok": false,
    "error": "not permitted to signal PID 910"
  }
]
`
	if rendered := buf.String(); rendered != want {
		t.Fatalf("--json output:\n%s\nwant:\n%s", rendered, want)
	}
}

func TestKillJSONEmptyIsAnEmptyArray(t *testing.T) {
	var out bytes.Buffer
	if err := writeKillJSON(&out, nil); err != nil {
		t.Fatalf("writeKillJSON: %v", err)
	}
	if rendered := out.String(); rendered != "[]\n" {
		t.Fatalf("rendered %q, want an empty array", rendered)
	}
}

func TestReportKillJSONExitsNonZeroOnFailure(t *testing.T) {
	var buf bytes.Buffer
	err := reportKill(&buf, killResults(), nil, true, false)
	if !errors.Is(err, errSilent) {
		t.Fatalf("err = %v, want the silent sentinel so --json output stands alone", err)
	}
	if !strings.HasPrefix(buf.String(), "[") {
		t.Fatal("the full result list must still be printed")
	}

	ok := killResults()[:3]
	if err := reportKill(&bytes.Buffer{}, ok, nil, true, false); err != nil {
		t.Fatalf("err = %v, want nil when every target succeeded", err)
	}
}

// TestReportKillTreatsAnAlreadyGoneTargetAsStopped: the verdict is about the
// end state, not about the action. A process that had already exited is not a
// failure — `down` used to print "1 of 3 targets failed" for exactly this case
// while everything really was stopped.
func TestReportKillTreatsAnAlreadyGoneTargetAsStopped(t *testing.T) {
	display.NoColor = true
	results := []killer.Result{
		{Host: state.LocalhostName, Port: 3000, BindAddress: "127.0.0.1", PID: 300,
			Name: "vite", Method: state.MethodSIGTERM, OK: true},
		{Host: state.LocalhostName, PID: 400, Name: "npm", Method: state.MethodNone,
			OK: false, Error: "no process with PID 400", Code: killer.CodeNotFound},
	}
	var buf bytes.Buffer
	if err := reportKill(&buf, results, nil, false, false); err != nil {
		t.Fatalf("err = %v, want nil: nothing was asked for that is not already true", err)
	}
	got := buf.String()
	if !strings.Contains(got, "nothing to stop: no process with PID 400") {
		t.Errorf("the row should say it was already gone, not shout error:\n%s", got)
	}
	if !strings.Contains(got, "2/2 stopped.") {
		t.Errorf("both rows count as stopped:\n%s", got)
	}
}

func TestReportKillText(t *testing.T) {
	display.NoColor = true
	table := []ports.ListeningPort{
		{Port: 3100, BindAddress: "127.0.0.1", PID: 310},
		{Port: 5433, BindAddress: "0.0.0.0", PID: 910},
	}
	var buf bytes.Buffer
	err := reportKill(&buf, killResults(), table, false, false)
	if err == nil {
		t.Fatal("a failed target must make the command exit 1")
	}
	rendered := buf.String()
	for _, expected := range []string{
		"stopped (SIGTERM) bundler (PID 410) on port 3100",
		"stopped (SIGKILL) devserver (PID 310) on port 3100",
		"stopped container postgres on port 5433",
		"error: not permitted to signal PID 910",
		"released http://127.0.0.1:3100",
		"released http://localhost:5433",
		"3/4 stopped.",
	} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("output is missing %q:\n%s", expected, rendered)
		}
	}
}

func TestReportKillDryRunSaysWould(t *testing.T) {
	display.NoColor = true
	var buf bytes.Buffer
	if err := reportKill(&buf, killResults()[:1], nil, false, true); err != nil {
		t.Fatal(err)
	}
	rendered := buf.String()
	if !strings.Contains(rendered, "Dry run: 1 action(s), children first.") {
		t.Errorf("missing the dry-run header:\n%s", rendered)
	}
	if !strings.Contains(rendered, "would stop (SIGTERM) bundler") {
		t.Errorf("a dry run reports what would happen:\n%s", rendered)
	}
	if strings.Contains(rendered, "stopped.") || strings.Contains(rendered, "released") {
		t.Errorf("a dry run must not claim anything happened:\n%s", rendered)
	}
}

// ------------------------------------------------------------- selectors ---

func resetKillSelectors(t *testing.T) {
	t.Helper()
	killPIDFlag, killGroupFlag, killAllFlag = nil, "", false
	t.Cleanup(func() {
		killPIDFlag, killGroupFlag, killAllFlag = nil, "", false
	})
}

func killTable() []ports.ListeningPort {
	return []ports.ListeningPort{
		{Port: 3100, PID: 310, BindAddress: "127.0.0.1", Group: "shopfront", GroupSource: "auto"},
		{Port: 3101, PID: 311, BindAddress: "127.0.0.1", Tag: "edge", RunID: "run9"},
		{Port: 5433, PID: 910, BindAddress: "0.0.0.0", Type: ports.PortTypeDocker,
			DockerContainer: "postgres", DockerComposeProject: "shop-app"},
		{Port: 7100, PID: 710, BindAddress: "127.0.0.1"},
	}
}

func TestKillTargetsByPortAndPID(t *testing.T) {
	resetKillSelectors(t)
	killPIDFlag = []int{12345}

	targets, confirm, err := killTargets([]string{"3100"}, killTable(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if confirm {
		t.Error("an explicit port or pid needs no confirmation")
	}
	want := []killer.Target{{PID: 12345}, {Port: 3100, BindAddress: "127.0.0.1"}}
	if len(targets) != 2 || targets[0] != want[0] || targets[1] != want[1] {
		t.Fatalf("targets = %+v, want %+v", targets, want)
	}
}

func TestKillTargetsByGroup(t *testing.T) {
	snapshot := killTable()
	cases := map[string][]int{
		"shopfront": {3100}, // the scanner's group field
		"edge":      {3101}, // an option-berth run tag
		"run9":      {3101}, // or its run id
		"shop-app":  {5433}, // a Docker Compose project
		"SHOP-APP":  {5433}, // case-insensitively
	}
	for group, expectedPorts := range cases {
		resetKillSelectors(t)
		killGroupFlag = group
		selected, confirm, err := killTargets(nil, snapshot, "")
		if err != nil {
			t.Fatalf("group %q: %v", group, err)
		}
		if !confirm {
			t.Errorf("group %q: a group kill must confirm unless --yes", group)
		}
		if len(selected) != len(expectedPorts) || selected[0].Port != expectedPorts[0] {
			t.Errorf("group %q: targets = %+v, want ports %v", group, selected, expectedPorts)
		}
	}
}

func TestKillTargetsUnknownGroup(t *testing.T) {
	resetKillSelectors(t)
	killGroupFlag = "nope"
	if _, _, err := killTargets(nil, killTable(), ""); err == nil {
		t.Fatal("an unknown group must be an error, not an empty sweep")
	}
}

func TestKillTargetsAllIncludesEveryListener(t *testing.T) {
	resetKillSelectors(t)
	killAllFlag = true
	targets, confirm, err := killTargets(nil, killTable(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !confirm {
		t.Error("--all must confirm unless --yes")
	}
	if len(targets) != 4 {
		t.Fatalf("targets = %+v, want every listening port", targets)
	}
}

func TestKillTargetsRejectsMixedSelectors(t *testing.T) {
	resetKillSelectors(t)
	killGroupFlag = "shopfront"
	if _, _, err := killTargets([]string{"3100"}, killTable(), ""); err == nil {
		t.Error("--group with a positional port must be rejected")
	}

	resetKillSelectors(t)
	killAllFlag = true
	if _, _, err := killTargets([]string{"3100"}, killTable(), ""); err == nil {
		t.Error("--all with a positional port must be rejected")
	}

	resetKillSelectors(t)
	if _, _, err := killTargets(nil, killTable(), ""); err == nil {
		t.Error("no selector at all must be rejected")
	}

	resetKillSelectors(t)
	if _, _, err := killTargets([]string{"http"}, killTable(), ""); err == nil {
		t.Error("a non-numeric argument must be rejected")
	}
}

func TestPositionalTargetReadsAPortFirst(t *testing.T) {
	for _, tc := range []struct {
		name      string
		n         int
		alive     bool
		port, pid int
		probes    int
	}{
		{"listener wins over same PID", 3100, true, 3100, 0, 0},
		{"no listener or process", 4321, false, 4321, 0, 1},
		{"live PID without listener", 4321, true, 0, 4321, 1},
		{"outside port range", 70000, false, 0, 70000, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got := positionalTargetWithProbe(tc.n, killTable(), "127.0.0.1", func(pid int) bool {
				calls++
				if pid != tc.n {
					t.Fatal(pid)
				}
				return tc.alive
			})
			if got.Port != tc.port || got.PID != tc.pid || calls != tc.probes {
				t.Fatalf("target=%+v probes=%d; expected port=%d PID=%d probes=%d", got, calls, tc.port, tc.pid, tc.probes)
			}
			if got.Port != 0 && got.BindAddress != "127.0.0.1" {
				t.Fatal("bind selector lost")
			}
		})
	}
}

// A `.oberth.yaml` group only exists after groups.Attribute has run over the
// scan, so `oberth kill -g` misses it unless the kill path enriches the same way
// `oberth list` does. This pins the composition the scan path relies on.
func TestKillTargetsSeeAFileGroup(t *testing.T) {
	// The index resolves symlinks; on macOS t.TempDir() is one, so compare
	// like for like.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving the temp dir: %v", err)
	}
	manifest := []byte("name: cart\nports:\n  - 4200\n")
	if err := os.WriteFile(filepath.Join(root, groups.ConfigName), manifest, 0o644); err != nil {
		t.Fatalf("writing the manifest: %v", err)
	}

	table := []ports.ListeningPort{
		{Port: 4200, PID: 420, BindAddress: "127.0.0.1", Process: "node", Cwd: root},
	}
	if before := table[0].Group; before != "" {
		t.Fatalf("group = %q before attribution, want it empty", before)
	}
	groups.Attribute(table)
	if after := table[0].Group; after != "cart" {
		t.Fatalf("group = %q after attribution, want cart", after)
	}

	resetKillSelectors(t)
	killGroupFlag = "cart"
	selected, _, err := killTargets(nil, table, "")
	if err != nil {
		t.Fatalf("kill -g cart: %v", err)
	}
	expected := killer.Target{Port: 4200, BindAddress: "127.0.0.1"}
	if len(selected) != 1 || selected[0] != expected {
		t.Fatalf("targets = %+v, want %+v", selected, expected)
	}
}

// --------------------------------------------------- observation failure ---

func blindScan() error {
	return &ports.ObservationError{
		Code:   ports.CodeObservationBlind,
		Detail: "the collector cannot see its own listener",
	}
}

// TestKillSelectionRefusesABlindTable: an unreadable listener table is not an
// empty table. `kill --all` must report the observation failure instead of
// selecting nothing and exiting 0 as if everything had already stopped.
func TestKillSelectionRefusesABlindTable(t *testing.T) {
	resetKillSelectors(t)
	killAllFlag = true

	snapshot, targets, confirm, complete, err := killSelection(context.Background(), nil, "", func(context.Context) ([]ports.ListeningPort, error) {
		return nil, blindScan()
	})
	if err == nil {
		t.Fatal("a failed scan must not become an empty selection")
	}
	if complete {
		t.Fatal("a failed scan was reported as a complete observation")
	}
	var oe *ports.ObservationError
	if !errors.As(err, &oe) || oe.Code != ports.CodeObservationBlind {
		t.Fatalf("err = %v, want the collector's observation_blind code", err)
	}
	if snapshot != nil || targets != nil || confirm {
		t.Fatalf("snapshot=%+v targets=%+v confirm=%v, want no selection at all", snapshot, targets, confirm)
	}
}

// TestKillSelectionPassesTheCommandContextToTheScan: the selection read is the
// command's observation, so it must be cancellable with the command. A scan
// that ignored the context would keep a stopped command waiting on a slow
// collector. The hook records the context; no real collector runs.
func TestKillSelectionPassesTheCommandContextToTheScan(t *testing.T) {
	resetKillSelectors(t)
	killPIDFlag = []int{4242}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var got context.Context
	_, _, _, _, err := killSelection(ctx, nil, "", func(scanCtx context.Context) ([]ports.ListeningPort, error) {
		got = scanCtx
		return nil, blindScan()
	})
	if err != nil {
		t.Fatalf("an explicit pid must not be denied by a failed observation: %v", err)
	}
	if got != ctx {
		t.Fatal("the scan did not receive the command context")
	}
}

// TestKillAllWithABlindTableIsTheDocumentedCLIFailure drives the real command
// all the way to the exit contract: `kill --all --json` on a process that cannot
// see its own listeners must write one JSON error document to stderr, nothing to
// stdout, and exit 1 with the collector's own stable code. It is the user-visible
// half of "cannot see is not nothing is running": the alternative is `[]` on
// stdout and a zero exit, which reads as "everything was already stopped".
func TestKillAllWithABlindTableIsTheDocumentedCLIFailure(t *testing.T) {
	resetRouting(t)
	// dry-run stays false: the contract is the real command's fail-closed
	// exit, not a planned no-op. Isolation, not dry-run, is what keeps a
	// missed hook from signalling anything.
	withKillFlags(t, func() { killAllFlag, killJSONFlag, killYesFlag = true, true, true })
	// No daemon: the command takes the direct path, which is the one that scans.
	noDaemonReachable(t)
	// The hook the direct path actually calls is scanForKill, not scanPorts
	// (that seam belongs to status/group observation). A missed injection
	// stays on the package test default, which refuses ports.Scan.
	isolateKillScan(t, func(context.Context) ([]ports.ListeningPort, error) {
		return nil, blindScan()
	})
	isolateKillExecutor(t, refuseKillExecutorIfReached)
	// The error is rendered by the root command after the subcommand returns, so
	// the JSON mode has to be on there too.
	prevJSON := jsonMode
	jsonMode = true
	t.Cleanup(func() { jsonMode = prevJSON })

	cmd := &cobra.Command{RunE: runKill}
	cmd.Flags().String("ip", "", "")
	cmd.SetContext(context.Background())

	var runErr error
	stdout := captureStdout(t, func() { runErr = runKill(cmd, nil) })
	if runErr == nil {
		t.Fatal("a blind table answered --all with success")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing: the failure is not a result list", stdout)
	}
	if code := exitCodeFor(runErr); code != exitFail {
		t.Fatalf("exit code = %d, want %d", code, exitFail)
	}

	var stderr bytes.Buffer
	if status := reportError(&stderr, runErr); status != exitFail {
		t.Fatalf("reportError = %d, want %d", status, exitFail)
	}
	var doc struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Hint    string `json:"hint"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &doc); err != nil {
		t.Fatalf("stderr is not one JSON document: %v\n%s", err, stderr.String())
	}
	if doc.Error.Code != ports.CodeObservationBlind {
		t.Fatalf("error.code = %q, want %q", doc.Error.Code, ports.CodeObservationBlind)
	}
	if !strings.Contains(doc.Error.Message, "cannot see its own listener") {
		t.Fatalf("error.message = %q, want the collector's own detail", doc.Error.Message)
	}
}

// anOwnedRunRow is what the collector reports for one listener of a run the
// registry knows: the row a scan produces before attribution stamps the run's
// identity on it. The pid-only path has to keep that identity, because it is
// what decides the stop's scope and what a reader is shown.
func anOwnedRunRow(port, pid, rootPID int) ports.ListeningPort {
	return ports.ListeningPort{
		Port: port, PID: pid, BindAddress: "127.0.0.1", Type: ports.PortTypeUser,
		Process: "cmd.test", Command: "/tmp/cmd.test -test.run=helper",
		RunID: "run-1", RunGroup: "storefront", Tag: "api", RunRootPID: rootPID,
	}
}

// TestKillSelectionResolvesAnExplicitPIDAgainstTheAttributedTable: naming a pid
// does not make the listener table irrelevant. The enriched table is what
// carries a listener's run ownership — the root whose tree the stop covers, the
// service name a reader sees, and the address a report can say was released — so
// `kill --pid <a run's listener>` must resolve against it exactly like every
// other selector. Reading it only for the selectors that pick their targets out
// of it made this one form stop a leaf instead of its run, and name the process
// instead of the service.
func TestKillSelectionResolvesAnExplicitPIDAgainstTheAttributedTable(t *testing.T) {
	resetKillSelectors(t)
	killPIDFlag = []int{4242}
	table := []ports.ListeningPort{anOwnedRunRow(3300, 4242, 4300)}

	scanned := 0
	snapshot, targets, confirm, complete, err := killSelection(context.Background(), nil, "", func(context.Context) ([]ports.ListeningPort, error) {
		scanned++
		return table, nil
	})
	if err != nil {
		t.Fatalf("resolving an explicit pid: %v", err)
	}
	if !complete {
		t.Fatal("a successful table read was not reported as complete")
	}
	if scanned != 1 {
		t.Fatalf("the listener table was read %d times, want the attribution read once", scanned)
	}
	if len(snapshot) != 1 || snapshot[0].RunRootPID != 4300 || snapshot[0].Tag != "api" {
		t.Fatalf("snapshot = %+v, want the row that names the run", snapshot)
	}
	if confirm || len(targets) != 1 || targets[0].PID != 4242 {
		t.Fatalf("targets = %+v confirm=%v, want the named pid only", targets, confirm)
	}
}

// TestKillSelectionKeepsAnExplicitPIDWhenTheTableIsUnreadable pins the one
// exception at the boundary where it belongs: a failed observation must not veto
// a pid the caller named, because the pid is the whole subject. It is not an
// exception to reading the table — that read is what the test above requires —
// and it is not a promise that the kill can proceed without observation: the
// killer still observes before it signals, so a blind machine still ends in a
// failed row rather than a silent success.
func TestKillSelectionKeepsAnExplicitPIDWhenTheTableIsUnreadable(t *testing.T) {
	resetKillSelectors(t)
	killPIDFlag = []int{12345}

	snapshot, targets, confirm, complete, err := killSelection(context.Background(), nil, "", func(context.Context) ([]ports.ListeningPort, error) {
		return nil, blindScan()
	})
	if err != nil {
		t.Fatalf("an explicit pid must not be denied by a failed observation: %v", err)
	}
	if complete {
		t.Fatal("a failed observation was reported as complete")
	}
	if snapshot != nil {
		t.Fatalf("snapshot = %+v, want no rows to have been read", snapshot)
	}
	if confirm || len(targets) != 1 || targets[0].PID != 12345 {
		t.Fatalf("targets = %+v confirm=%v, want the named pid only", targets, confirm)
	}
}

// TestKillSelectionKeepsTheRowsAPartialReadProduced: a partial table is still
// evidence about the listeners it does show. For an explicit pid that evidence
// is the run attribution, so the readable row travels with the pid instead of
// being discarded — the pid itself is signalled either way.
func TestKillSelectionKeepsTheRowsAPartialReadProduced(t *testing.T) {
	resetKillSelectors(t)
	killPIDFlag = []int{4242}
	partial := []ports.ListeningPort{anOwnedRunRow(3300, 4242, 4300)}

	snapshot, targets, _, complete, err := killSelection(context.Background(), nil, "", func(context.Context) ([]ports.ListeningPort, error) {
		return partial, &ports.ObservationError{
			Code: ports.CodeObservationIncomplete, Detail: "ss: unreadable row", Partial: true, Rows: partial,
		}
	})
	if err != nil {
		t.Fatalf("a partial table must not deny an explicit pid: %v", err)
	}
	if complete {
		t.Fatal("a partial read was reported as a complete observation")
	}
	if len(snapshot) != 1 || snapshot[0].RunRootPID != 4300 {
		t.Fatalf("snapshot = %+v, want the row the collector did read", snapshot)
	}
	if len(targets) != 1 || targets[0].PID != 4242 {
		t.Fatalf("targets = %+v, want the named pid", targets)
	}
}

// withKillFlags installs one flag combination and puts every kill flag back
// afterwards. resetKillSelectors only clears the selectors, while these tests also
// set the confirmation, output and signalling flags.
func withKillFlags(t *testing.T, set func()) {
	t.Helper()
	oldPID, oldGroup, oldSession := killPIDFlag, killGroupFlag, killSessionFlag
	oldAll, oldTree, oldForce := killAllFlag, killTreeFlag, killForceFlag
	oldGrace, oldNoEscalate, oldYes := killGraceFlag, killNoEscalateFlag, killYesFlag
	oldDry, oldJSON := killDryRunFlag, killJSONFlag
	t.Cleanup(func() {
		killPIDFlag, killGroupFlag, killSessionFlag = oldPID, oldGroup, oldSession
		killAllFlag, killTreeFlag, killForceFlag = oldAll, oldTree, oldForce
		killGraceFlag, killNoEscalateFlag, killYesFlag = oldGrace, oldNoEscalate, oldYes
		killDryRunFlag, killJSONFlag = oldDry, oldJSON
	})
	killPIDFlag, killGroupFlag, killSessionFlag = nil, "", ""
	killAllFlag, killTreeFlag, killForceFlag = false, false, false
	killGraceFlag, killNoEscalateFlag, killYesFlag = killer.DefaultGrace, false, true
	killDryRunFlag, killJSONFlag = false, false
	set()
}

// TestKillConfirmationShowsThePlanAndStopsNothingWithoutAConsent pins the
// confirm round: the plan the executor produced is printed, and an answer that
// is not yes leaves the process running.
func TestKillConfirmationShowsThePlanAndStopsNothingWithoutAConsent(t *testing.T) {
	resetRouting(t)
	// A sweep is the selection broad enough to be confirmed; a port named on
	// its own is not.
	withKillFlags(t, func() {
		killAllFlag, killYesFlag = true, false
	})
	noDaemonReachable(t)
	answerNextPrompt(t)

	port := startListener(t)
	var killErr error
	out := captureStdout(t, func() {
		killErr = killDirect(context.Background(), nil, "")
	})
	if killErr != nil {
		t.Fatalf("kill under confirmation: %v", killErr)
	}
	if !strings.Contains(out, "Will stop ") || !strings.Contains(out, "Proceed? [y/N]") {
		t.Fatalf("stdout = %q, want the plan it asks about", out)
	}
	if !strings.Contains(out, "Aborted.") {
		t.Fatalf("stdout = %q, want the abort sentence", out)
	}
	conn, dialErr := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 300*time.Millisecond)
	if dialErr != nil {
		t.Fatalf("the listener is gone: it was stopped without a consent (%v)", dialErr)
	}
	conn.Close()
}

// TestKillSessionCurrentWithoutAnAgentFails: `--session current` resolves from
// this shell's environment, and there is nothing to resolve outside an agent.
func TestKillSessionCurrentWithoutAnAgentFails(t *testing.T) {
	withKillFlags(t, func() { killSessionFlag = "current" })
	for _, key := range []string{
		sessions.EnvSession, sessions.EnvSessionID, sessions.EnvClaudeCode,
		sessions.EnvClaudeCodeSessionID, sessions.EnvClaudeSessionID,
		sessions.EnvCodexThreadID, sessions.EnvCodexSandbox, sessions.EnvCursorAgent,
		sessions.EnvCursorAgentSessionID,
	} {
		t.Setenv(key, "")
	}

	err := killSession(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no agent session") {
		t.Fatalf("err = %v, want the missing-session refusal", err)
	}
}

// TestKillSessionNeedsTheDaemon: a session only exists in the daemon's run
// registry, so the selector says so instead of falling back to a local scan.
func TestKillSessionNeedsTheDaemon(t *testing.T) {
	resetRouting(t)
	withKillFlags(t, func() { killSessionFlag = "sess-1" })
	prev := dialDaemon
	dialDaemon = func(context.Context) (*client.Client, error) { return nil, client.ErrNotRunning }
	t.Cleanup(func() { dialDaemon = prev })

	err := killSession(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sessions need a running daemon") {
		t.Fatalf("err = %v, want the daemon requirement", err)
	}
}

// TestKillSessionReportsASessionTheDaemonDoesNotKnow: the daemon owns session
// membership, so its not_found for an unknown id is the answer the caller gets.
func TestKillSessionReportsASessionTheDaemonDoesNotKnow(t *testing.T) {
	resetRouting(t)
	withKillFlags(t, func() { killSessionFlag = "sess-nobody" })
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket harness does not apply to named pipes")
	}
	startScanningDaemon(t)

	err := killSession(context.Background())
	if err == nil {
		t.Fatal("killing an unknown session returned nil")
	}
	if code, _, _ := describe(err); code != "session_not_found" {
		t.Fatalf("code = %q (%v), want the daemon's own session_not_found", code, err)
	}
}
