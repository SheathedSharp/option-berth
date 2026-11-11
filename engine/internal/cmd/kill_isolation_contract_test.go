package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/killer"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// These contracts are pure memory: no listener, subprocess, daemon, scan, ps,
// signal or real killer is reachable from this file. Every hook installed here is
// a closure that only counts and returns rows or errors it built in memory, and
// every snapshot row and target is a synthetic, nonzero identity that is never
// handed to a real executor.

// requirePreinstalledKillHooks fails unless the hooks already installed are
// exactly the fail-closed pair the test binary's init installs. It compares
// function identities and never calls a hook, so the guard can read whatever is
// in place without risking a production ports.Scan or killer.KillPorts, and it
// installs nothing: a deleted or broken init is reported instead of being masked
// by re-assigning the expected values.
func requirePreinstalledKillHooks(t *testing.T) {
	t.Helper()
	if scanForKill == nil || reflect.ValueOf(scanForKill).Pointer() != reflect.ValueOf(refuseUnscopedKillScan).Pointer() {
		t.Fatalf("scanForKill is not the preinstalled refusing scanner; the test-binary init in kill_isolation_test.go must install refuseUnscopedKillScan, or a missed replacement reaches the real ports.Scan")
	}
	if killHere == nil || reflect.ValueOf(killHere).Pointer() != reflect.ValueOf(refuseUnscopedKillSignal).Pointer() {
		t.Fatalf("killHere is not the preinstalled refusing executor; the test-binary init in kill_isolation_test.go must install refuseUnscopedKillSignal, or a missed replacement reaches the real killer.KillPorts")
	}
	if guardDaemonKill == nil || reflect.ValueOf(guardDaemonKill).Pointer() != reflect.ValueOf(refuseUnscopedDaemonKill).Pointer() {
		t.Fatalf("guardDaemonKill is not the preinstalled refusing daemon guard; the test-binary init in kill_isolation_test.go must install refuseUnscopedDaemonKill, or a forgotten daemon fixture reaches an arbitrary daemon's kill RPC")
	}
}

// TestKillHookDefaultsRefuseMissingHooks pins both layers of the fail-closed
// default: the preinstalled hooks are the refusing sentinels, and a killDirect
// that was not given replacements stops at whichever seam its selector needs
// instead of observing the machine or signalling a process. The scan refusal and
// the signal refusal are different errors, so each selector has to reach its own
// boundary. The identity guard runs first, so the calls below can only ever reach
// the refusing sentinels.
func TestKillHookDefaultsRefuseMissingHooks(t *testing.T) {
	requirePreinstalledKillHooks(t)

	if _, err := scanForKill(); !errors.Is(err, errUnscopedKillScan) {
		t.Fatalf("preinstalled scan hook err = %v, want %v", err, errUnscopedKillScan)
	}
	if _, err := killHere(context.Background(), []killer.Target{{Port: 3100}}, killer.Options{}, false); !errors.Is(err, errUnscopedKillSignal) {
		t.Fatalf("preinstalled signal hook err = %v, want %v", err, errUnscopedKillSignal)
	}

	for _, tc := range []struct {
		name string
		set  func()
		args []string
		want error
	}{
		// A positional port resolves through the listener table, so the missing
		// scan hook is the answer and the executor is never reached.
		{"a positional port stops at the scan", func() {}, []string{"3100"}, errUnscopedKillScan},
		// A named pid survives the unreadable table as a selection, and then the
		// missing signal hook is what stops it.
		{"a named pid stops at the signal", func() { killPIDFlag = []int{4242} }, nil, errUnscopedKillSignal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withKillFlags(t, tc.set)

			var runErr error
			out := captureStdout(t, func() {
				runErr = killDirect(context.Background(), tc.args, "")
			})
			if !errors.Is(runErr, tc.want) {
				t.Fatalf("killDirect err = %v, want %v", runErr, tc.want)
			}
			if out != "" {
				t.Fatalf("stdout = %q, want nothing: a refusal is not a result list", out)
			}
		})
	}
}

// These daemon-route contracts are pure memory too: the guard and the registry
// only look at a client pointer, and a *client.Client zero value is never
// dialled, called or closed here. A refusal is required to be the sentinel, so
// a round that reached the RPC would fail the contract with a different answer.

// fakeDaemonKillClient is an instance identity, never a connection: it is only
// ever compared by pointer and never handed to Call.
func fakeDaemonKillClient() *client.Client { return &client.Client{} }

// fakeDaemonKillScan is an in-memory Scan that is never invoked; the guard only
// requires that a fixture supplied one.
func fakeDaemonKillScan() func(scanner.Include) ([]ports.ListeningPort, error) {
	return func(scanner.Include) ([]ports.ListeningPort, error) {
		return nil, errors.New("pure contract: the fixture scan must not be called by the guard")
	}
}

// TestKillDaemonRoutesRefuseWithoutABoundFixture: with the preinstalled guard and
// no fixture binding, both daemon executors must stop before any RPC, in a real
// round and in a dry run, so a forgotten fixture cannot reach an arbitrary or
// shared daemon even when the round is only a plan.
func TestKillDaemonRoutesRefuseWithoutABoundFixture(t *testing.T) {
	requirePreinstalledKillHooks(t)

	for _, tc := range []struct {
		name   string
		dryRun bool
	}{
		{"a real round", false},
		{"a dry run", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeDaemonKillClient()
			targets := []killer.Target{{Port: 3100, PID: 4242}}

			results, err := killInDaemon(c)(context.Background(), targets, killer.Options{}, tc.dryRun)
			if !errors.Is(err, errUnscopedDaemonKill) {
				t.Fatalf("killInDaemon err = %v, want %v", err, errUnscopedDaemonKill)
			}
			if results != nil {
				t.Fatalf("killInDaemon results = %+v, want nothing: a refusal is not a kill result", results)
			}

			results, err = killSessionInDaemon(c, "sess-synthetic")(context.Background(), nil, killer.Options{}, tc.dryRun)
			if !errors.Is(err, errUnscopedDaemonKill) {
				t.Fatalf("killSessionInDaemon err = %v, want %v", err, errUnscopedDaemonKill)
			}
			if results != nil {
				t.Fatalf("killSessionInDaemon results = %+v, want nothing: a refusal is not a kill result", results)
			}
		})
	}
}

// TestKillDaemonRoutesRefuseAnUnboundClientInstance binds one fixture client and
// drives the routes with a different instance: instance scope, not target or
// pid, is what the guard checks, so the bound client does not lend its
// authorization to anyone else and naming a pid on the round changes nothing.
func TestKillDaemonRoutesRefuseAnUnboundClientInstance(t *testing.T) {
	requirePreinstalledKillHooks(t)

	bound, stranger := fakeDaemonKillClient(), fakeDaemonKillClient()
	if err := bindDaemonKillClient(bound, fakeDaemonKillScan()); err != nil {
		t.Fatalf("binding the fixture client: %v", err)
	}
	t.Cleanup(func() { releaseDaemonKillClient(bound) })

	if err := guardDaemonKill(bound); err != nil {
		t.Fatalf("the bound fixture instance was refused: %v", err)
	}

	targets := []killer.Target{{PID: 4242}}
	if _, err := killInDaemon(stranger)(context.Background(), targets, killer.Options{}, false); !errors.Is(err, errUnscopedDaemonKill) {
		t.Fatalf("killInDaemon on an unbound instance err = %v, want %v", err, errUnscopedDaemonKill)
	}
	if _, err := killSessionInDaemon(stranger, "sess-synthetic")(context.Background(), nil, killer.Options{}, false); !errors.Is(err, errUnscopedDaemonKill) {
		t.Fatalf("killSessionInDaemon on an unbound instance err = %v, want %v", err, errUnscopedDaemonKill)
	}
}

// TestKillDaemonFixtureBindingRequiresAnExplicitScan: the fixture boundary
// refuses a nil client and a missing Scan, so no daemon with a fallback scanner
// is ever constructed or authorized. A released instance is refused again.
func TestKillDaemonFixtureBindingRequiresAnExplicitScan(t *testing.T) {
	requirePreinstalledKillHooks(t)

	if err := bindDaemonKillClient(nil, fakeDaemonKillScan()); !errors.Is(err, errUnscopedDaemonKill) {
		t.Fatalf("bindDaemonKillClient(nil, scan) err = %v, want %v", err, errUnscopedDaemonKill)
	}
	if err := bindDaemonKillClient(fakeDaemonKillClient(), nil); !errors.Is(err, errUnscopedDaemonKill) {
		t.Fatalf("bindDaemonKillClient(client, nil) err = %v, want %v", err, errUnscopedDaemonKill)
	}
	if err := guardDaemonKill(nil); !errors.Is(err, errUnscopedDaemonKill) {
		t.Fatalf("guardDaemonKill(nil) err = %v, want %v", err, errUnscopedDaemonKill)
	}

	c := fakeDaemonKillClient()
	if err := bindDaemonKillClient(c, fakeDaemonKillScan()); err != nil {
		t.Fatalf("binding a fixture client: %v", err)
	}
	if err := guardDaemonKill(c); err != nil {
		t.Fatalf("the bound fixture instance was refused: %v", err)
	}
	releaseDaemonKillClient(c)
	if err := guardDaemonKill(c); !errors.Is(err, errUnscopedDaemonKill) {
		t.Fatalf("guardDaemonKill after release err = %v, want %v", err, errUnscopedDaemonKill)
	}
}

// TestKillDaemonExecutorsUseInjectedGuard pins the injection point itself: both
// daemon executors must consult whatever guard the seam currently holds, on the
// instance they were built with, in a real round. The installed guard is a fake
// that returns its own sentinel, so an executor that skipped the seam would
// return a different answer (or a real RPC error), each path's error is required
// to be that exact sentinel, and the count proves both routes ran it once.
// Nothing here dials, calls, scans or builds a fixture: the client is inert.
func TestKillDaemonExecutorsUseInjectedGuard(t *testing.T) {
	requirePreinstalledKillHooks(t)

	injected := errors.New("injected guard: this round was refused by the installed fake")
	c := fakeDaemonKillClient()
	calls := 0

	previous := guardDaemonKill
	t.Cleanup(func() { guardDaemonKill = previous })
	guardDaemonKill = func(got *client.Client) error {
		calls++
		if got != c {
			t.Errorf("guard received client %p, want the inert instance %p the executor was built with", got, c)
		}
		return injected
	}

	targets := []killer.Target{{Port: 3100, PID: 4242}}

	results, err := killInDaemon(c)(context.Background(), targets, killer.Options{}, false)
	if err != injected {
		t.Fatalf("killInDaemon err = %v, want the injected guard error %v", err, injected)
	}
	if results != nil {
		t.Fatalf("killInDaemon results = %+v, want nothing: a refusal is not a kill result", results)
	}

	results, err = killSessionInDaemon(c, "sess-synthetic")(context.Background(), nil, killer.Options{}, false)
	if err != injected {
		t.Fatalf("killSessionInDaemon err = %v, want the injected guard error %v", err, injected)
	}
	if results != nil {
		t.Fatalf("killSessionInDaemon results = %+v, want nothing: a refusal is not a kill result", results)
	}

	if calls != 2 {
		t.Fatalf("the installed guard ran %d time(s), want exactly once per route (2)", calls)
	}
}

// syntheticAttributedRow is one listener built in memory, already carrying the
// run attribution the production scan leaves on a row. The contract under test
// is what killDirect forwards, not how attribution is computed.
func syntheticAttributedRow(port, pid, rootPID int, tag, runID string) ports.ListeningPort {
	return ports.ListeningPort{
		Port: port, PID: pid, BindAddress: "127.0.0.1", Type: ports.PortTypeUser,
		Tag: tag, RunID: runID, RunRootPID: rootPID,
	}
}

// countingKillScan installs a scanner that answers from memory and counts its
// calls. It returns the counter the test asserts on.
func countingKillScan(t *testing.T, rows ...ports.ListeningPort) *int {
	t.Helper()
	calls := new(int)
	isolateKillScan(t, func() ([]ports.ListeningPort, error) {
		(*calls)++
		return append([]ports.ListeningPort(nil), rows...), nil
	})
	return calls
}

// recordedKillRounds is a fake executor that records every round the command
// asks for and answers from memory. It never calls signalListenersForKill or
// killer.KillPorts, so the only rows any test sees are the ones it was given.
type recordedKillRounds struct {
	targets [][]killer.Target
	opts    []killer.Options
	dryRuns []bool
}

func (r *recordedKillRounds) hook(results ...killer.Result) killExecutor {
	return func(_ context.Context, targets []killer.Target, opts killer.Options, dryRun bool) ([]killer.Result, error) {
		r.targets = append(r.targets, append([]killer.Target(nil), targets...))
		copied := opts
		copied.Ports = append([]ports.ListeningPort(nil), opts.Ports...)
		r.opts = append(r.opts, copied)
		r.dryRuns = append(r.dryRuns, dryRun)
		return results, nil
	}
}

// only is the single round the command must have run, for contracts where more
// than one round means either a confirmation it should not have asked for or a
// retry the command must not perform.
func (r *recordedKillRounds) only(t *testing.T) ([]killer.Target, killer.Options, bool) {
	t.Helper()
	if len(r.dryRuns) != 1 {
		t.Fatalf("the installed executor ran %d time(s), want exactly once", len(r.dryRuns))
	}
	return r.targets[0], r.opts[0], r.dryRuns[0]
}

// requireAttributedSnapshot checks the executor's Ports is the scanned row with
// its run attribution intact. A nil Ports would let the killer take its own
// global scan, which is exactly what this file must make unreachable.
func requireAttributedSnapshot(t *testing.T, opts killer.Options, row ports.ListeningPort) {
	t.Helper()
	if opts.Ports == nil {
		t.Fatal("executor Ports = nil: the killer would fall back to its own global scan")
	}
	if len(opts.Ports) != 1 {
		t.Fatalf("executor Ports = %+v, want the one synthetic row", opts.Ports)
	}
	got := opts.Ports[0]
	if got.Port != row.Port || got.PID != row.PID || got.BindAddress != row.BindAddress {
		t.Fatalf("executor row = %+v, want the scanned listener %+v", got, row)
	}
	if got.RunRootPID != row.RunRootPID || got.Tag != row.Tag || got.RunID != row.RunID {
		t.Fatalf("executor row = %+v, want RunRootPID %d, Tag %q and RunID %q preserved",
			got, row.RunRootPID, row.Tag, row.RunID)
	}
}

// requireOneReportedRow checks the command printed the fake executor's own row.
// A real executor could not have produced this identity, so the stdout below is
// proof the report came from the fake.
func requireOneReportedRow(t *testing.T, out string, want killer.Result) {
	t.Helper()
	var reported []killer.Result
	if err := json.Unmarshal([]byte(out), &reported); err != nil {
		t.Fatalf("stdout is not the result list: %v\n%s", err, out)
	}
	if len(reported) != 1 || reported[0].PID != want.PID || reported[0].Name != want.Name || !reported[0].OK {
		t.Fatalf("reported rows = %+v, want the in-memory row the fake executor returned", reported)
	}
}

// TestKillDirectForwardsTheAttributedSnapshotForAPositionalPort drives the actual
// no-daemon path with a fake scanner and a fake executor: the supplied port
// resolves against the attributed table, and the executor receives that very
// snapshot with the real round (not a dry-run plan) requested.
func TestKillDirectForwardsTheAttributedSnapshotForAPositionalPort(t *testing.T) {
	withKillFlags(t, func() {
		killDryRunFlag, killJSONFlag, killYesFlag = false, true, true
	})

	row := syntheticAttributedRow(3100, 4242, 4240, "api", "run-synthetic")
	scanned := countingKillScan(t, row)
	rounds := &recordedKillRounds{}
	isolateKillExecutor(t, rounds.hook(killer.Result{
		Host: state.LocalhostName, Port: row.Port, BindAddress: row.BindAddress,
		PID: row.PID, Name: row.Tag, Method: state.MethodSIGTERM, OK: true,
	}))

	var runErr error
	out := captureStdout(t, func() {
		runErr = killDirect(context.Background(), []string{"3100"}, "")
	})
	if runErr != nil {
		t.Fatalf("killDirect on installed fake hooks: %v", runErr)
	}
	if *scanned != 1 {
		t.Fatalf("the installed scanner ran %d time(s), want exactly once", *scanned)
	}

	targets, opts, dryRun := rounds.only(t)
	if dryRun {
		t.Fatal("the executor was asked for a plan, not the real round; a dry run cannot prove the safety path")
	}
	if len(targets) != 1 || targets[0].Port != row.Port {
		t.Fatalf("targets = %+v, want the synthetic port %d", targets, row.Port)
	}
	requireAttributedSnapshot(t, opts, row)
	requireOneReportedRow(t, out, killer.Result{PID: row.PID, Name: row.Tag})
}

// TestKillDirectForwardsTheAttributedSnapshotForAPIDOnlySelection is the same
// contract for the selector the direct path used to resolve blind: an explicit
// --pid still reads the attributed table, so the executor gets that snapshot
// (RunRootPID and Tag included, never nil) together with the pid-only target, and
// the real round is requested. The pid is synthetic and is never passed to a real
// executor.
func TestKillDirectForwardsTheAttributedSnapshotForAPIDOnlySelection(t *testing.T) {
	const syntheticPID, syntheticRoot = 4242, 4240
	withKillFlags(t, func() {
		killDryRunFlag, killJSONFlag, killYesFlag = false, true, true
		killPIDFlag = []int{syntheticPID}
	})

	row := syntheticAttributedRow(3100, syntheticPID, syntheticRoot, "api", "run-synthetic")
	scanned := countingKillScan(t, row)
	rounds := &recordedKillRounds{}
	isolateKillExecutor(t, rounds.hook(killer.Result{
		Host: state.LocalhostName, Port: row.Port, BindAddress: row.BindAddress,
		PID: syntheticPID, Name: row.Tag, Method: state.MethodSIGTERM, OK: true,
	}))

	var runErr error
	out := captureStdout(t, func() {
		runErr = killDirect(context.Background(), nil, "")
	})
	if runErr != nil {
		t.Fatalf("killDirect --pid on installed fake hooks: %v", runErr)
	}
	if *scanned != 1 {
		t.Fatalf("the installed scanner ran %d time(s), want exactly once for the attribution read", *scanned)
	}

	targets, opts, dryRun := rounds.only(t)
	if dryRun {
		t.Fatal("the executor was asked for a plan, not the real round; a dry run cannot prove the safety path")
	}
	if len(targets) != 1 || targets[0].PID != syntheticPID || targets[0].Port != 0 {
		t.Fatalf("targets = %+v, want the named pid only", targets)
	}
	requireAttributedSnapshot(t, opts, row)
	requireOneReportedRow(t, out, killer.Result{PID: syntheticPID, Name: row.Tag})
}

// TestKillDirectBlindSelectionStopsBeforeTheExecutor: a failed observation is the
// selection's answer, not an empty table. With a blind synthetic scan, killDirect
// must return the collector's own error and the executor must never be entered,
// so a selection that could not see anything cannot turn into a signal.
func TestKillDirectBlindSelectionStopsBeforeTheExecutor(t *testing.T) {
	withKillFlags(t, func() {})

	scanned := 0
	isolateKillScan(t, func() ([]ports.ListeningPort, error) {
		scanned++
		return nil, &ports.ObservationError{
			Code:   ports.CodeObservationBlind,
			Detail: "synthetic collector cannot see its own listener",
		}
	})

	rounds := &recordedKillRounds{}
	isolateKillExecutor(t, rounds.hook())

	var runErr error
	out := captureStdout(t, func() {
		runErr = killDirect(context.Background(), []string{"3100"}, "")
	})

	if scanned != 1 {
		t.Fatalf("the installed scanner ran %d time(s), want exactly once", scanned)
	}
	if len(rounds.dryRuns) != 0 {
		t.Fatalf("the executor ran %d time(s) on a blind selection, want 0", len(rounds.dryRuns))
	}
	if runErr == nil {
		t.Fatal("a blind selection answered with success")
	}
	var oe *ports.ObservationError
	if !errors.As(runErr, &oe) || oe.Code != ports.CodeObservationBlind {
		t.Fatalf("err = %v, want the synthetic observation_blind failure", runErr)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want nothing: the failure is not a result list", out)
	}
}

// These B2 and B3 contracts call only guardIsolatedKillScope with synthetic
// rows and captured identities. They never install fixtures, never call
// isolatedEnrichedSnapshot, never walk a process table, and never reach
// signalListenersForKill or a production adapter. A passing admission test is
// therefore not a license to signal.

var isolatedSyntheticBirth = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func syntheticOwnership(pids ...int) isolatedKillOwnership {
	owned := isolatedKillOwnership{identities: map[int]isolatedProcessIdentity{}}
	for _, pid := range pids {
		owned.identities[pid] = claimedKillIdentity(pid, isolatedSyntheticBirth)
	}
	return owned
}

func syntheticRow(port, pid, rootPID int) ports.ListeningPort {
	return ports.ListeningPort{
		Port: port, PID: pid, BindAddress: "127.0.0.1",
		Type: ports.PortTypeUser, RunRootPID: rootPID,
		StartedAt: isolatedSyntheticBirth.Format(time.RFC3339Nano),
	}
}

func syntheticTarget(pid int) killer.Target {
	return killer.Target{PID: pid, StartedAt: isolatedSyntheticBirth}
}

// TestKillScopeRefusesUnownedRunRootPID: owning the listener identity is not
// enough when the row names a different RunRootPID. The production killer would
// root the unit there and force a tree, so the scope check must refuse.
func TestKillScopeRefusesUnownedRunRootPID(t *testing.T) {
	owned := syntheticOwnership(4242)
	row := syntheticRow(3100, 4242, 9999)
	err := guardIsolatedKillScope(owned, []killer.Target{{Port: 3100, PID: 4242, StartedAt: isolatedSyntheticBirth}}, killer.Options{Ports: []ports.ListeningPort{row}})
	if !errors.Is(err, errUnscopedKillScope) {
		t.Fatalf("guardIsolatedKillScope err = %v, want %v", err, errUnscopedKillScope)
	}
}

// TestKillScopeRefusesDockerContainerMetadata: a docker-typed or container-
// labeled row, including PID 0, is refused unless that exact container id was
// declared. A container name that only appears on the row is not ownership.
func TestKillScopeRefusesDockerContainerMetadata(t *testing.T) {
	owned := syntheticOwnership(4242)
	for _, tc := range []struct {
		name string
		row  ports.ListeningPort
	}{
		{"pid-zero container row", ports.ListeningPort{Port: 3100, Type: ports.PortTypeDocker, DockerContainer: "ctr-synthetic"}},
		{"typed docker without an id", ports.ListeningPort{Port: 3100, PID: 4242, Type: ports.PortTypeDocker, StartedAt: isolatedSyntheticBirth.Format(time.RFC3339Nano)}},
		{"container id on a user-typed row", ports.ListeningPort{Port: 3100, PID: 4242, Type: ports.PortTypeUser, DockerContainer: "ctr-synthetic", StartedAt: isolatedSyntheticBirth.Format(time.RFC3339Nano)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := guardIsolatedKillScope(owned, []killer.Target{{Port: tc.row.Port}}, killer.Options{Ports: []ports.ListeningPort{tc.row}})
			if !errors.Is(err, errUnscopedKillScope) {
				t.Fatalf("guardIsolatedKillScope err = %v, want %v", err, errUnscopedKillScope)
			}
		})
	}
}

// TestKillScopeRefusesTreeOrGroupWideningBeyondDeclaredMembers: a tree plan
// that names a member the fixture did not capture is refused. The check does
// not invent members from a live process table.
func TestKillScopeRefusesTreeOrGroupWideningBeyondDeclaredMembers(t *testing.T) {
	owned := syntheticOwnership(4242, 4240)
	row := syntheticRow(3100, 4242, 4240)
	err := guardIsolatedKillScope(
		owned,
		[]killer.Target{syntheticTarget(4242), syntheticTarget(7777)},
		killer.Options{Ports: []ports.ListeningPort{row}, Tree: true},
	)
	if !errors.Is(err, errUnscopedKillScope) {
		t.Fatalf("guardIsolatedKillScope err = %v, want %v", err, errUnscopedKillScope)
	}
}

// TestKillScopeAdmitsDeclaredRootAndMembersWithoutCallingTheRealExecutor: the
// same metadata is admitted when every implied PID has a captured identity and
// a matching birth claim. Success here is only the scope function returning
// nil; this test has no executor hook.
func TestKillScopeAdmitsDeclaredRootAndMembersWithoutCallingTheRealExecutor(t *testing.T) {
	owned := syntheticOwnership(4242, 4240)
	row := syntheticRow(3100, 4242, 4240)
	err := guardIsolatedKillScope(
		owned,
		[]killer.Target{{Port: 3100, PID: 4242, StartedAt: isolatedSyntheticBirth}},
		killer.Options{Ports: []ports.ListeningPort{row}, Tree: true},
	)
	if err != nil {
		t.Fatalf("declared root and members were refused: %v", err)
	}
}

// TestKillScopeRefusesAReusedPIDWithADifferentBirthClaim: a later occupant of
// the same numeric pid is a different identity and must not inherit the
// fixture's authorization.
func TestKillScopeRefusesAReusedPIDWithADifferentBirthClaim(t *testing.T) {
	owned := syntheticOwnership(4242)
	reused := isolatedSyntheticBirth.Add(time.Hour)
	row := syntheticRow(3100, 4242, 0)
	row.StartedAt = reused.Format(time.RFC3339Nano)
	err := guardIsolatedKillScope(owned, []killer.Target{{Port: 3100, PID: 4242, StartedAt: reused}}, killer.Options{Ports: []ports.ListeningPort{row}})
	if !errors.Is(err, errUnscopedKillScope) {
		t.Fatalf("guardIsolatedKillScope err = %v, want %v", err, errUnscopedKillScope)
	}
}

// TestKillScopeRefusesAnInvalidatedFixturePID: once the fixture is marked
// ended, the captured pid must not authorize a process, even one that presents
// the original birth claim.
func TestKillScopeRefusesAnInvalidatedFixturePID(t *testing.T) {
	owned := invalidateIsolatedProcessIdentity(syntheticOwnership(4242), 4242)
	row := syntheticRow(3100, 4242, 0)
	err := guardIsolatedKillScope(owned, []killer.Target{{Port: 3100, PID: 4242, StartedAt: isolatedSyntheticBirth}}, killer.Options{Ports: []ports.ListeningPort{row}})
	if !errors.Is(err, errUnscopedKillScope) {
		t.Fatalf("guardIsolatedKillScope err = %v, want %v", err, errUnscopedKillScope)
	}
}

// TestKillScopeRefusesANumericPIDWithoutABirthClaim: a pid number with no
// birth claim is not ownership, even when a captured identity exists for that
// number. The guard must not treat a live or recycled occupant as the fixture.
func TestKillScopeRefusesANumericPIDWithoutABirthClaim(t *testing.T) {
	owned := syntheticOwnership(4242)
	row := ports.ListeningPort{Port: 3100, PID: 4242, BindAddress: "127.0.0.1", Type: ports.PortTypeUser}
	err := guardIsolatedKillScope(owned, []killer.Target{{Port: 3100, PID: 4242}}, killer.Options{Ports: []ports.ListeningPort{row}})
	if !errors.Is(err, errUnscopedKillScope) {
		t.Fatalf("guardIsolatedKillScope err = %v, want %v", err, errUnscopedKillScope)
	}
}

// TestKillScopeAdmitsAHeldIdentityWithoutLiveProcessLookup: a held create-time
// handle plus a matching birth claim is enough. The handle is a synthetic
// *os.Process value, not a started process, and no process table is consulted.
func TestKillScopeAdmitsAHeldIdentityWithoutLiveProcessLookup(t *testing.T) {
	id := heldKillIdentity(&os.Process{Pid: 4242}, isolatedSyntheticBirth)
	owned := isolatedKillOwnership{identities: map[int]isolatedProcessIdentity{4242: id}}
	row := syntheticRow(3100, 4242, 0)
	err := guardIsolatedKillScope(owned, []killer.Target{{Port: 3100, PID: 4242, StartedAt: isolatedSyntheticBirth}}, killer.Options{Ports: []ports.ListeningPort{row}})
	if err != nil {
		t.Fatalf("held identity was refused: %v", err)
	}
}

// These B4 contracts call only cleanupHeldIdentity / killOwnedIdentities with
// synthetic identities and replacement hooks. They never start a process, never
// walk a process table, and never reach the default signal seams.

// TestKillCleanupRefusesABareOrClaimedPIDWithoutAHeldHandle: a birth claim
// without a create-time handle, or a zero identity, must refuse. The cleanup
// hooks must not be entered, because a fallback would be a bare pid signal.
func TestKillCleanupRefusesABareOrClaimedPIDWithoutAHeldHandle(t *testing.T) {
	calls := 0
	withKillCleanupHooks(t, func(isolatedProcessIdentity) error {
		calls++
		t.Fatal("cleanup hook ran for an identity with no held handle")
		return nil
	}, func(isolatedProcessIdentity) error {
		calls++
		t.Fatal("process-group hook ran for an identity with no held handle")
		return nil
	})

	for _, tc := range []struct {
		name string
		id   isolatedProcessIdentity
	}{
		{"zero identity", isolatedProcessIdentity{}},
		{"claimed birth only", claimedKillIdentity(4242, isolatedSyntheticBirth)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := killOwnedIdentities(tc.id)
			if !errors.Is(err, errUnscopedKillCleanup) {
				t.Fatalf("killOwnedIdentities err = %v, want %v", err, errUnscopedKillCleanup)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("cleanup hooks ran %d time(s), want 0: a refusal must not fall back to a signal", calls)
	}
}

// TestKillCleanupRefusesAnInvalidatedHeldIdentity: once the fixture has ended,
// cleanup must not signal through the stale handle or a recycled numeric pid.
func TestKillCleanupRefusesAnInvalidatedHeldIdentity(t *testing.T) {
	withKillCleanupHooks(t, func(isolatedProcessIdentity) error {
		t.Fatal("cleanup hook ran for an invalidated identity")
		return nil
	}, func(isolatedProcessIdentity) error {
		t.Fatal("process-group hook ran for an invalidated identity")
		return nil
	})
	id := heldKillIdentity(&os.Process{Pid: 4242}, isolatedSyntheticBirth)
	id.invalid = true
	err := cleanupHeldIdentity(id)
	if !errors.Is(err, errUnscopedKillCleanup) {
		t.Fatalf("cleanupHeldIdentity err = %v, want %v", err, errUnscopedKillCleanup)
	}
}

// TestKillCleanupRefusesAMismatchedHeldHandle: a handle whose pid is not the
// captured identity is not ownership, and must not be used as a numeric fallback.
func TestKillCleanupRefusesAMismatchedHeldHandle(t *testing.T) {
	withKillCleanupHooks(t, func(isolatedProcessIdentity) error {
		t.Fatal("cleanup hook ran for a mismatched handle")
		return nil
	}, func(isolatedProcessIdentity) error {
		t.Fatal("process-group hook ran for a mismatched handle")
		return nil
	})
	id := heldKillIdentity(&os.Process{Pid: 4242}, isolatedSyntheticBirth)
	id.PID = 7777
	err := cleanupHeldIdentity(id)
	if !errors.Is(err, errUnscopedKillCleanup) {
		t.Fatalf("cleanupHeldIdentity err = %v, want %v", err, errUnscopedKillCleanup)
	}
}

// TestKillCleanupUsesHeldHandleAndGroupHooksWithoutAProcessTable: a still-valid
// held process-group identity is cleaned up only through the installed hooks.
// Success is hook invocation, not a live signal or a descendant walk.
func TestKillCleanupUsesHeldHandleAndGroupHooksWithoutAProcessTable(t *testing.T) {
	var procs, groups []int
	withKillCleanupHooks(t, func(id isolatedProcessIdentity) error {
		procs = append(procs, id.PID)
		return nil
	}, func(id isolatedProcessIdentity) error {
		groups = append(groups, id.PID)
		return nil
	})
	id := heldKillProcessGroup(&os.Process{Pid: 4242}, isolatedSyntheticBirth)
	if err := killOwnedIdentities(id); err != nil {
		t.Fatalf("held process-group cleanup was refused: %v", err)
	}
	if len(procs) != 1 || procs[0] != 4242 {
		t.Fatalf("held-process hook pids = %v, want [4242]", procs)
	}
	if len(groups) != 1 || groups[0] != 4242 {
		t.Fatalf("process-group hook pids = %v, want [4242]", groups)
	}
}

// These S5 contracts prove the registered-only snapshot never enters the
// enrichment hook, and that isolatedEnrichedSnapshot is the sole opt-in path.
// The hook is a recorder; it does not call ports.Enrich or groups.Attribute.

// TestIsolatedRegisteredSnapshotDoesNotCallEnrichment: a registered synthetic
// fixture is visible as the exact row that was stored. The enrichment hook
// must not run for isolatedRegisteredSnapshot or for the default hooks
// installIsolatedKillHooks installs on scanForKill.
func TestIsolatedRegisteredSnapshotDoesNotCallEnrichment(t *testing.T) {
	calls := 0
	withEnrichIsolatedSnapshotHook(t, func(rows []ports.ListeningPort) []ports.ListeningPort {
		calls++
		t.Fatal("enrichIsolatedSnapshot ran on the registered-only path")
		return rows
	})
	row := syntheticRow(3100, 4242, 0)
	registerIsolatedKillFixture(t, row, claimedKillIdentity(4242, isolatedSyntheticBirth))

	got := isolatedRegisteredSnapshot(t)
	if len(got) != 1 || got[0].Port != row.Port || got[0].PID != row.PID || got[0].StartedAt != row.StartedAt {
		t.Fatalf("registered snapshot = %+v, want the exact fixture row %+v", got, row)
	}

	scanned, err := scanForKill()
	if err != nil {
		t.Fatalf("installed registered scan: %v", err)
	}
	if len(scanned) != 1 || scanned[0].PID != row.PID || scanned[0].Port != row.Port {
		t.Fatalf("scanForKill = %+v, want the registered row", scanned)
	}
	if calls != 0 {
		t.Fatalf("enrichment hook ran %d time(s), want 0", calls)
	}
}

// TestIsolatedEnrichedSnapshotIsTheOptInEnrichmentPath: only the isolated
// enriched helper calls the enrichment hook, and it is given the registered
// rows. The hook itself does not invoke production Enrich or Attribute.
func TestIsolatedEnrichedSnapshotIsTheOptInEnrichmentPath(t *testing.T) {
	var seen []ports.ListeningPort
	calls := 0
	withEnrichIsolatedSnapshotHook(t, func(rows []ports.ListeningPort) []ports.ListeningPort {
		calls++
		seen = append([]ports.ListeningPort(nil), rows...)
		return rows
	})
	row := syntheticRow(3100, 4242, 0)
	registerIsolatedKillFixture(t, row, claimedKillIdentity(4242, isolatedSyntheticBirth))

	if got := isolatedRegisteredSnapshot(t); len(got) != 1 || calls != 0 {
		t.Fatalf("registered snapshot called enrichment %d time(s); rows = %+v", calls, got)
	}

	got := isolatedEnrichedSnapshot(t)
	if calls != 1 || len(seen) != 1 || seen[0].PID != row.PID || seen[0].Port != row.Port {
		t.Fatalf("enrichment hook ran %d time(s) and saw %+v, want the registered row %+v once", calls, seen, row)
	}
	if len(got) != 1 || got[0].PID != row.PID {
		t.Fatalf("isolatedEnrichedSnapshot = %+v, want the hook's returned registered row", got)
	}

	useIsolatedEnrichedKillScan(t)
	scanned, err := scanForKill()
	if err != nil {
		t.Fatalf("installed enriched scan: %v", err)
	}
	if len(scanned) != 1 || scanned[0].PID != row.PID || calls != 2 {
		t.Fatalf("opt-in scanForKill enrichment count = %d, rows = %+v; want 2 hook calls", calls, scanned)
	}
}
