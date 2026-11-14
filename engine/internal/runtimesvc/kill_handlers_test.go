package runtimesvc

import (
	"context"
	"net"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/killer"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/sessions"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func ptr[T any](v T) *T { return &v }

// killHarness is the pair every handler test below needs: a harness whose fake
// scan the test owns, and a client talking to it. Both hang off a context that
// ends with the test.
func killHarness(t *testing.T) (*testHarness, *testClient) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	harness := newHarness(t, ctx)
	return harness, harness.dial(ctx)
}

// snapshotWith builds the snapshot the kill handlers resolve against.
func snapshotWith(pp ...state.Port) state.Snapshot {
	return state.Snapshot{Ports: pp}
}

func TestSelectorTargetRequiresExactlyOneKey(t *testing.T) {
	empty := snapshotWith()
	cases := []struct {
		name     string
		selector rpc.Selector
	}{
		{name: "empty", selector: rpc.Selector{}},
		{name: "two keys", selector: rpc.Selector{Port: ptr(3000), PID: ptr(42)}},
		{name: "bind only", selector: rpc.Selector{BindAddress: ptr("127.0.0.1")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := selectorTarget(tc.selector, empty)
			if err == nil {
				t.Fatal("want an invalid_selector error")
			}
			if code := killer.Code(err); code != killer.CodeInvalidSelector {
				t.Fatalf("code = %q, want invalid_selector", code)
			}
		})
	}
}

func TestSelectorTargetPinsAnUnambiguousBindAddress(t *testing.T) {
	snapshot := snapshotWith(state.Port{Port: 3000, BindAddress: "127.0.0.1"})
	target, err := selectorTarget(rpc.Selector{Port: ptr(3000)}, snapshot)
	if err != nil {
		t.Fatalf("selectorTarget: %v", err)
	}
	if target.Port != 3000 {
		t.Errorf("port = %d, want the 3000 the selector named", target.Port)
	}
	if target.BindAddress != "127.0.0.1" {
		t.Errorf("bind_address = %q, want the snapshot's bind address", target.BindAddress)
	}
}

func TestSelectorTargetLeavesAMultiBindPortOpen(t *testing.T) {
	snapshot := snapshotWith(
		state.Port{Port: 3000, BindAddress: "127.0.0.1"},
		state.Port{Port: 3000, BindAddress: "::1"},
	)
	target, err := selectorTarget(rpc.Selector{Port: ptr(3000)}, snapshot)
	if err != nil {
		t.Fatalf("selectorTarget: %v", err)
	}
	if target.BindAddress != "" {
		t.Fatalf("bind_address = %q, want it left to the killer to report as ambiguous", target.BindAddress)
	}
}

func TestSelectorTargetCarriesTheOtherSelectorKeys(t *testing.T) {
	snapshot := snapshotWith()
	byPID, err := selectorTarget(rpc.Selector{PID: ptr(4242)}, snapshot)
	if err != nil || byPID.PID != 4242 {
		t.Fatalf("pid selector = %+v, %v", byPID, err)
	}
	byRun, err := selectorTarget(rpc.Selector{RunID: ptr("run-1")}, snapshot)
	if err != nil || byRun.RunID != "run-1" {
		t.Fatalf("run_id selector = %+v, %v", byRun, err)
	}
}

func TestGroupTargetsMatchesGroupRunAndComposeProject(t *testing.T) {
	snapshot := snapshotWith(
		state.Port{Port: 3000, BindAddress: "127.0.0.1", Group: ptr("my-app")},
		state.Port{Port: 3001, BindAddress: "127.0.0.1", Run: &state.Run{Group: "MY-APP"}},
		state.Port{Port: 5432, BindAddress: "0.0.0.0", Docker: &state.Docker{ComposeProject: "my-app"}},
		state.Port{Port: 9999, BindAddress: "127.0.0.1", Group: ptr("other")},
	)
	targets := groupTargets(snapshot, "my-app")
	if len(targets) != 3 {
		t.Fatalf("targets = %+v, want the three my-app rows", targets)
	}
	for _, target := range targets {
		if target.Port == 9999 {
			t.Fatalf("group kill picked up an unrelated port: %+v", targets)
		}
	}
}

func TestRunsWithoutPortsLeavesOnlySilentRuns(t *testing.T) {
	snapshot := snapshotWith(
		state.Port{Port: 3000, PID: 100, Group: ptr("demo"), Run: &state.Run{RootPID: 100}},
		state.Port{Port: 3001, PID: 101, Run: &state.Run{Group: "demo", RootPID: 101}},
	)
	silent := runsWithoutPorts(snapshot, "demo", []int{100, 101, 200})
	if len(silent) != 1 || silent[0] != 200 {
		t.Fatalf("pids = %v, want only the silent run", silent)
	}
}

func TestKillEnvelopeListsAffectedKeysOnce(t *testing.T) {
	envelope := killEnvelope([]state.KillResult{
		{Host: state.LocalhostName, Port: 3000, BindAddress: "127.0.0.1", PID: 400,
			Method: state.MethodSIGTERM, OK: true},
		{Host: state.LocalhostName, Port: 3000, BindAddress: "127.0.0.1", PID: 300,
			Method: state.MethodSIGKILL, OK: true},
		{Host: state.LocalhostName, Port: 0, PID: 12345, Method: state.MethodSIGTERM, OK: true},
	})
	if !envelope.OK {
		t.Error("ok = false, want true when every row succeeded")
	}
	if len(envelope.Affected) != 1 {
		t.Fatalf("affected = %v, want one port key", envelope.Affected)
	}
	if key := envelope.Affected[0]; key != "3000:127.0.0.1" {
		t.Errorf("affected = %q, want the key the two 3000 rows share", key)
	}
}

func TestKillEnvelopeIsNotOKWhenARowFailed(t *testing.T) {
	envelope := killEnvelope([]state.KillResult{
		{Host: state.LocalhostName, Port: 3000, BindAddress: "127.0.0.1", PID: 400,
			Method: state.MethodSIGTERM, OK: true},
		{Host: state.LocalhostName, Port: 8080, BindAddress: "127.0.0.1",
			Method: state.MethodNone, OK: false, Error: "boom"},
	})
	if envelope.OK {
		t.Error("ok = true, want false when a row failed")
	}
	if len(envelope.Affected) != 1 {
		t.Fatalf("affected = %v, want only the successful row", envelope.Affected)
	}
	if key := envelope.Affected[0]; key != "3000:127.0.0.1" {
		t.Errorf("affected = %q, want the successful row's key", key)
	}
}

func TestKillEnvelopeAlwaysHasArrays(t *testing.T) {
	envelope := killEnvelope(nil)
	if envelope.Results == nil || envelope.Affected == nil {
		t.Fatalf("envelope = %+v, want empty arrays rather than null", envelope)
	}
}

func TestPortsKillRejectsEmptyTargets(t *testing.T) {
	_, cl := killHarness(t)

	rpcErr := cl.call("ports.kill", rpc.PortsKillParams{}, nil)
	if rpcErr == nil {
		t.Fatal("ports.kill with no targets succeeded")
	}
	if rpcErr.Code != rpc.CodeInvalidParams {
		t.Fatalf("code = %d, want %d", rpcErr.Code, rpc.CodeInvalidParams)
	}
}

func TestPortsKillRejectsAnInvalidSelector(t *testing.T) {
	_, cl := killHarness(t)

	rpcErr := cl.call("ports.kill", rpc.PortsKillParams{Targets: []rpc.Selector{{}}}, nil)
	if rpcErr == nil {
		t.Fatal("ports.kill with an empty selector succeeded")
	}
	if rpcErr.Code != rpc.CodeInvalidSelector || rpcErr.Data.Code != "invalid_selector" {
		t.Fatalf("error = %d/%q, want 1009/invalid_selector", rpcErr.Code, rpcErr.Data.Code)
	}
}

// An unknown port is a not_found row, and not_found is **not** a failure: the
// end state the caller asked for — nothing listening there — already holds. The
// envelope stays ok, and the row is kept so the reader sees why nothing was
// signalled.
func TestPortsKillDryRunReportsAnUnknownPortAsNothingToStop(t *testing.T) {
	_, cl := killHarness(t)

	port := freeTestPort(t)
	var envelope rpc.KillEnvelope
	params := rpc.PortsKillParams{Targets: []rpc.Selector{{Port: ptr(port)}}, DryRun: true}
	if rpcErr := cl.call("ports.kill", params, &envelope); rpcErr != nil {
		t.Fatalf("ports.kill: %v", rpcErr)
	}
	if !envelope.OK {
		t.Errorf("ok = false; a port nothing listens on is already in the wanted state")
	}
	if len(envelope.Results) != 1 {
		t.Fatalf("results = %+v, want the single row the port resolved to", envelope.Results)
	}
	row := envelope.Results[0]
	if row.Method != state.MethodNone || row.OK {
		t.Fatalf("results[0] = %+v, want a none row that says it was not there", row)
	}
	if row.Code != killer.CodeNotFound {
		t.Errorf("code = %q, want %q so the reader does not parse the prose", row.Code, killer.CodeNotFound)
	}
	if len(envelope.Affected) != 0 {
		t.Errorf("affected = %v, want nothing affected", envelope.Affected)
	}
}

// TestKillEnvelopeStaysOKWhenTheTargetWasAlreadyGone is the same rule one level
// down: a process that had already exited does not make the envelope fail.
// `down` on a project whose last process was gone used to report a failure
// while everything really was stopped.
func TestKillEnvelopeStaysOKWhenTheTargetWasAlreadyGone(t *testing.T) {
	envelope := killEnvelope([]state.KillResult{
		{Host: state.LocalhostName, Port: 3000, BindAddress: "127.0.0.1", PID: 300,
			Method: state.MethodSIGTERM, OK: true},
		{Host: state.LocalhostName, PID: 400, Method: state.MethodNone, OK: false,
			Error: "no process with PID 400", Code: killer.CodeNotFound},
	})
	if !envelope.OK {
		t.Error("ok = false; an already-gone target is not a failure")
	}
	if len(envelope.Results) != 2 {
		t.Fatalf("results = %+v, want the row kept so the reader sees why", envelope.Results)
	}
}

// TestKillResolvesAgainstTheDaemonsOwnScan: the handler resolves targets
// against the rows it just scanned rather than letting killer.KillPorts scan
// the machine a second time inside the RPC. The proof is a listener that only
// exists in the daemon's scan — a killer that went looking for itself would
// find nothing on that port and answer a failed "none" row — so a planned
// signal carrying the snapshot's pid and name is the daemon's scan being used.
func TestKillResolvesAgainstTheDaemonsOwnScan(t *testing.T) {
	harness, cl := killHarness(t)

	const fakePID = 424242
	port := freeTestPort(t)
	harness.setRows(ports.ListeningPort{
		Port: port, BindAddress: "127.0.0.1", PID: fakePID,
		Process: "node", Display: "fake-api",
	})

	var envelope rpc.KillEnvelope
	params := rpc.PortsKillParams{Targets: []rpc.Selector{{Port: ptr(port)}}, DryRun: true}
	if rpcErr := cl.call("ports.kill", params, &envelope); rpcErr != nil {
		t.Fatalf("ports.kill: %v", rpcErr)
	}
	if len(envelope.Results) != 1 {
		t.Fatalf("results = %+v, want one row for the snapshot's listener", envelope.Results)
	}
	row := envelope.Results[0]
	if row.Method == state.MethodNone {
		t.Fatalf("row = %+v, want a planned signal: the killer resolved against its own scan, not the daemon's", row)
	}
	if row.PID != fakePID {
		t.Errorf("pid = %d, want the snapshot's fake pid %d", row.PID, fakePID)
	}
	if row.Name != "fake-api" || row.Port != port {
		t.Errorf("row = %+v, want the snapshot's name fake-api on port %d", row, port)
	}
}

// TestKillCostsExactlyOneScan pins the other half: resolving targets rescans
// once and nothing scans again behind it.
func TestKillCostsExactlyOneScan(t *testing.T) {
	harness, cl := killHarness(t)

	harness.setRows(ports.ListeningPort{Port: 4323, BindAddress: "127.0.0.1", PID: 424243, Process: "node"})
	if _, err := harness.loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatalf("priming: %v", err)
	}
	before := harness.loop.Status().Scans

	var envelope rpc.KillEnvelope
	params := rpc.PortsKillParams{Targets: []rpc.Selector{{Port: ptr(4323)}}, DryRun: true}
	if rpcErr := cl.call("ports.kill", params, &envelope); rpcErr != nil {
		t.Fatalf("ports.kill: %v", rpcErr)
	}
	if scans := harness.loop.Status().Scans; scans != before+1 {
		t.Errorf("scans = %d, want exactly one (target resolution) more than %d", scans, before)
	}
}

func TestGroupsKillReportsAnUnknownGroupAsNotFound(t *testing.T) {
	_, cl := killHarness(t)

	rpcErr := cl.call("groups.kill", rpc.GroupsKillParams{Name: "no-such-group"}, nil)
	if rpcErr == nil {
		t.Fatal("groups.kill on an unknown group succeeded")
	}
	if rpcErr.Code != rpc.CodeNotFound || rpcErr.Data.Code != "not_found" {
		t.Fatalf("error = %d/%q, want 1001/not_found", rpcErr.Code, rpcErr.Data.Code)
	}
	if rpcErr.Data.Hint == "" {
		t.Error("hint is empty; the contract asks for an actionable one")
	}
}

func TestGroupsKillRequiresAName(t *testing.T) {
	_, cl := killHarness(t)

	rpcErr := cl.call("groups.kill", rpc.GroupsKillParams{Name: "  "}, nil)
	if rpcErr == nil || rpcErr.Code != rpc.CodeInvalidParams {
		t.Fatalf("error = %v, want invalid_params", rpcErr)
	}
}

// downFixture is a project with a database of its own. Everything the file
// declares is the file's to stop: a service the machine owns is not declared
// here at all (decision 0010).
const downFixture = `name: demo
services:
  - name: db
    cmd: uv run db
    port: 53306
  - name: api
    cmd: uv run api
    port: 8000
`

// `down` stops the whole group — nothing in the manifest is exempt.
func TestDownStopsEveryDeclaredService(t *testing.T) {
	dir := resolvedDir(t, t.TempDir())
	writeConfig(t, dir, downFixture)

	harness, cl := killHarness(t)
	listener := func(port int) ports.ListeningPort {
		return ports.ListeningPort{
			Port: port, BindAddress: "127.0.0.1", PID: 53000 + port, Process: "listener", Cwd: dir,
		}
	}
	harness.setRows(listener(53306), listener(8000))
	if _, err := harness.loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatalf("priming the scanner: %v", err)
	}

	var envelope rpc.KillEnvelope
	if rpcErr := cl.call("groups.kill", rpc.GroupsKillParams{Name: "demo", Release: true, DryRun: true}, &envelope); rpcErr != nil {
		t.Fatalf("groups.kill: %v", rpcErr)
	}
	resolved := map[int]bool{}
	for _, row := range envelope.Results {
		resolved[row.Port] = true
	}
	if len(resolved) != 2 || !resolved[8000] || !resolved[53306] {
		t.Fatalf("down resolved %v, want both ports the file declares", resolved)
	}
}

func TestKillCapabilityIsAdvertised(t *testing.T) {
	for _, capability := range Capabilities() {
		if capability == "ports.kill" {
			return
		}
	}
	t.Fatalf("capabilities = %v, missing ports.kill", Capabilities())
}

// freeTestPort reserves a port and releases it, so nothing is listening on it.
func freeTestPort(t *testing.T) int {
	t.Helper()
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer reservation.Close()
	return reservation.Addr().(*net.TCPAddr).Port
}

// TestKillRepublishesSoTheNextReadIsFresh: a kill drops the cached scan, so the
// caller's own next `oberth list` does not still show the port it just freed,
// and the port_down row reaches the history ring with nobody subscribed
// .
func TestKillRepublishesSoTheNextReadIsFresh(t *testing.T) {
	harness, cl := killHarness(t)

	harness.setRows(ports.ListeningPort{Port: 4321, BindAddress: "127.0.0.1", PID: 999, Process: "node"})
	if _, err := harness.loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatalf("priming: %v", err)
	}
	before := harness.loop.Status().Scans

	// The kill itself fails (there is no pid 999), which is beside the point:
	// the handler still has to leave the cache invalidated.
	harness.setRows()
	var envelope rpc.KillEnvelope
	if rpcErr := cl.call("ports.kill",
		rpc.PortsKillParams{Targets: []rpc.Selector{{Port: ptr(4321)}}}, &envelope); rpcErr != nil {
		t.Fatalf("ports.kill: %v", rpcErr)
	}
	if scans := harness.loop.Status().Scans; scans <= before {
		t.Fatalf("scans = %d, want a rescan after the kill (was %d)", scans, before)
	}
	if cached := harness.loop.Cached(); len(cached.Ports) != 0 {
		t.Errorf("the cached snapshot still carries %d ports after the kill", len(cached.Ports))
	}
}

// TestGroupsKillResolvesAgainstAFreshScan: a group gaining a service inside the
// cache window must still be killed whole. `oberth up` starts three services in
// well under CacheTTL, and resolving `oberth kill -g` against the cached
// snapshot killed only the ones the last tick happened to see.
func TestGroupsKillResolvesAgainstAFreshScan(t *testing.T) {
	harness, cl := killHarness(t)
	// Ownership comes from the installed registry, not untrusted OS rows.
	harness.srv.runtime.SetRuns(&fakeRuns{live: []sessions.Live{
		{RunID: "run-1", PID: 900 + 4331, Group: "demo", Name: "svc-1"},
		{RunID: "run-2", PID: 900 + 4332, Group: "demo", Name: "svc-2"},
	}})

	inDemo := func(port int) ports.ListeningPort {
		return ports.ListeningPort{
			Port: port, BindAddress: "127.0.0.1", PID: 900 + port, Process: "listener",
		}
	}

	harness.setRows(inDemo(4331))
	if _, err := harness.loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatalf("priming: %v", err)
	}

	// A second service of the same group comes up right after that scan.
	harness.setRows(inDemo(4331), inDemo(4332))

	var envelope rpc.KillEnvelope
	if rpcErr := cl.call("groups.kill", rpc.GroupsKillParams{Name: "demo", DryRun: true}, &envelope); rpcErr != nil {
		t.Fatalf("groups.kill: %v", rpcErr)
	}
	if len(envelope.Results) != 2 {
		t.Fatalf("results = %+v, want a row for each of the group's two ports", envelope.Results)
	}
	seen := map[int]bool{}
	for _, row := range envelope.Results {
		seen[row.Port] = true
	}
	if !seen[4331] || !seen[4332] {
		t.Fatalf("killed ports %v, want both 4331 and 4332", seen)
	}
}

// TestDryRunKillDoesNotRepublish: resolving the targets costs one scan and a
// dry run stops right there — nothing changed, so nothing needs republishing.
func TestDryRunKillDoesNotRepublish(t *testing.T) {
	harness, cl := killHarness(t)

	harness.setRows(ports.ListeningPort{Port: 4322, BindAddress: "127.0.0.1", PID: 998, Process: "node"})
	if _, err := harness.loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatalf("priming: %v", err)
	}
	before := harness.loop.Status().Scans

	var envelope rpc.KillEnvelope
	params := rpc.PortsKillParams{Targets: []rpc.Selector{{Port: ptr(4322)}}, DryRun: true}
	if rpcErr := cl.call("ports.kill", params, &envelope); rpcErr != nil {
		t.Fatalf("ports.kill: %v", rpcErr)
	}
	if scans := harness.loop.Status().Scans; scans != before+1 {
		t.Errorf("scans = %d, want exactly one (target resolution) more than %d", scans, before)
	}
}
