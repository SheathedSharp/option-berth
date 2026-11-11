package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/killer"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
)

// Package-level fail-closed defaults apply only to the test binary. Production
// keeps observeListenersForKill / signalListenersForKill. A test that drives
// runKill or killDirect without installing a replacement cannot reach a global
// ports.Scan or killer.KillPorts.
func init() {
	scanForKill = refuseUnscopedKillScan
	killHere = refuseUnscopedKillSignal
	guardDaemonKill = refuseUnscopedDaemonKill
}

var (
	errUnscopedKillScan   = errors.New("test isolation: refused unscoped ports.Scan; install an isolated observation hook")
	errUnscopedKillSignal = errors.New("test isolation: refused unscoped killer.KillPorts; install an isolated executor hook")
	// errUnscopedKillScope is the refusal when a snapshot or target would let the
	// production killer widen past this test's declared PIDs or containers
	// (unowned RunRootPID, a process-tree member, or Docker metadata).
	errUnscopedKillScope = errors.New("test isolation: refused a kill plan that widens past declared fixture ownership")
	// errUnscopedKillCleanup is the refusal when harness cleanup has no still-
	// valid held create-time handle. Cleanup must not fall back to a bare pid
	// tree or a fresh process-table walk.
	errUnscopedKillCleanup = errors.New("test isolation: refused cleanup that would fall back to a bare pid tree")
)

func refuseUnscopedKillScan() ([]ports.ListeningPort, error) {
	return nil, errUnscopedKillScan
}

func refuseUnscopedKillSignal(context.Context, []killer.Target, killer.Options, bool) ([]killer.Result, error) {
	return nil, errUnscopedKillSignal
}

// errUnscopedDaemonKill is the refusal a daemon executor round gets when no
// test-owned fixture bound the client it was built with. It is a sentinel so a
// contract can prove the round failed at the guard, not at an RPC.
var errUnscopedDaemonKill = errors.New("test isolation: refused daemon kill round for a client no test-owned fixture bound")

// daemonKillScope is the test-only ownership proof for one daemon client: the
// instance a fixture dialled, together with the explicit Scan that fixture's
// daemon was built with. Keying on the client instance means two daemons in one
// test binary never authorize each other, and a round that names a pid or a
// selector is still refused because the guard never looks at targets.
type daemonKillScope struct {
	scan func(scanner.Include) ([]ports.ListeningPort, error)
}

var (
	daemonKillScopeMu sync.Mutex
	daemonKillScopes  = map[*client.Client]daemonKillScope{}
)

// bindDaemonKillClient records one instance and its explicit scan. A nil client
// or a nil scan is refused: a daemon that would fall back to its own scanner is
// exactly the thing the fixture rule exists to keep out.
func bindDaemonKillClient(c *client.Client, scan func(scanner.Include) ([]ports.ListeningPort, error)) error {
	if c == nil {
		return fmt.Errorf("%w: nil client", errUnscopedDaemonKill)
	}
	if scan == nil {
		return fmt.Errorf("%w: client %p was bound without an explicit Scan", errUnscopedDaemonKill, c)
	}
	daemonKillScopeMu.Lock()
	daemonKillScopes[c] = daemonKillScope{scan: scan}
	daemonKillScopeMu.Unlock()
	return nil
}

// releaseDaemonKillClient forgets an instance, so a later test cannot inherit
// another fixture's authorization.
func releaseDaemonKillClient(c *client.Client) {
	daemonKillScopeMu.Lock()
	delete(daemonKillScopes, c)
	daemonKillScopeMu.Unlock()
}

// refuseUnscopedDaemonKill is the test-binary guard installed by init. It admits
// only the client instances a fixture bound together with a Scan; every other
// client, including one a missed fixture dialled from a shared or real daemon,
// is refused before the executor reaches its RPC.
func refuseUnscopedDaemonKill(c *client.Client) error {
	if c == nil {
		return fmt.Errorf("%w: nil client", errUnscopedDaemonKill)
	}
	daemonKillScopeMu.Lock()
	scope, ok := daemonKillScopes[c]
	daemonKillScopeMu.Unlock()
	if !ok || scope.scan == nil {
		return fmt.Errorf("%w: client %p is not owned by a test fixture", errUnscopedDaemonKill, c)
	}
	return nil
}

// isolatedKillFixture is one listener this test started and may observe or
// signal. The snapshot is built from these rows; it is never a filtered view
// of a machine-wide ports.Scan. Ownership is the identities captured at
// registration, not a later live pid set.
type isolatedKillFixture struct {
	row        ports.ListeningPort
	identities []isolatedProcessIdentity
}

// isolatedProcessIdentity is the immutable ownership proof captured when the
// fixture was created: the pid at that moment, its birth time, and when the
// test started the process, the held *os.Process. A later occupant of the
// same number is a different identity. An invalidated fixture must not
// authorize anyone, including a recycled pid.
type isolatedProcessIdentity struct {
	PID          int
	StartedAt    time.Time
	Handle       *os.Process
	Held         bool
	ProcessGroup bool // this held process is the leader of a group we created at Start
	invalid      bool
}

// isolatedKillOwnership is the declared fixture set only: captured identities
// and container ids the test registered, never a live process-table expansion.
type isolatedKillOwnership struct {
	identities map[int]isolatedProcessIdentity
	containers map[string]bool
}

// heldKillIdentity is ownership of a process this test started and still holds.
func heldKillIdentity(proc *os.Process, startedAt time.Time) isolatedProcessIdentity {
	if proc == nil {
		return isolatedProcessIdentity{}
	}
	return isolatedProcessIdentity{PID: proc.Pid, StartedAt: startedAt.UTC(), Handle: proc, Held: true}
}

// heldKillProcessGroup is a held identity for a process this test started with
// Setpgid, so cleanup may signal that created group through the held leader.
// It still does not authorize a live process-table walk.
func heldKillProcessGroup(proc *os.Process, startedAt time.Time) isolatedProcessIdentity {
	id := heldKillIdentity(proc, startedAt)
	id.ProcessGroup = true
	return id
}

// claimedKillIdentity is a birth claim without a create-time handle. It is for
// in-memory contracts and for a child the test observed but did not Start. It
// is not a pidfd and does not claim to close check-then-signal TOCTOU.
func claimedKillIdentity(pid int, startedAt time.Time) isolatedProcessIdentity {
	return isolatedProcessIdentity{PID: pid, StartedAt: startedAt.UTC()}
}

func (id isolatedProcessIdentity) usable() bool {
	return id.PID > 0 && !id.StartedAt.IsZero() && !id.invalid && (id.Handle == nil || id.Handle.Pid == id.PID)
}

var isolatedKillMu sync.Mutex
var isolatedKillByTest = map[*testing.T][]isolatedKillFixture{}
var isolatedKillContainers = map[*testing.T]map[string]bool{}

func registerIsolatedKillFixture(t *testing.T, row ports.ListeningPort, ids ...isolatedProcessIdentity) {
	t.Helper()
	if row.Port == 0 && row.PID == 0 {
		t.Fatal("isolated fixture must name a port or a pid")
	}
	if len(ids) == 0 && row.PID != 0 {
		t.Fatal("registering a pid fixture requires a captured identity, not a bare pid")
	}
	copied := make([]isolatedProcessIdentity, 0, len(ids))
	for _, id := range ids {
		if !id.usable() {
			t.Fatal("fixture identity must include a pid and a birth time; a bare or invalidated pid is not ownership")
		}
		copied = append(copied, id)
	}
	if row.PID != 0 {
		found := false
		for _, id := range copied {
			if id.PID == row.PID {
				found = true
				if row.StartedAt == "" {
					row.StartedAt = id.StartedAt.UTC().Format(time.RFC3339Nano)
				}
				break
			}
		}
		if !found {
			t.Fatalf("fixture row pid %d has no captured identity", row.PID)
		}
	}
	isolatedKillMu.Lock()
	isolatedKillByTest[t] = append(isolatedKillByTest[t], isolatedKillFixture{row: row, identities: copied})
	isolatedKillMu.Unlock()
	t.Cleanup(func() {
		isolatedKillMu.Lock()
		delete(isolatedKillByTest, t)
		isolatedKillMu.Unlock()
	})
	installIsolatedKillHooks(t)
}

func fixturesFor(t *testing.T) []isolatedKillFixture {
	isolatedKillMu.Lock()
	defer isolatedKillMu.Unlock()
	return append([]isolatedKillFixture(nil), isolatedKillByTest[t]...)
}

// registerIsolatedKillContainer records one Docker container id this test owns.
// A ListeningPort that merely carries DockerContainer is not ownership; the
// production Enrich path must not be able to self-authorize a container.
func registerIsolatedKillContainer(t *testing.T, id string) {
	t.Helper()
	if id == "" {
		t.Fatal("isolated fixture container id must be non-empty")
	}
	isolatedKillMu.Lock()
	owned, ok := isolatedKillContainers[t]
	if !ok {
		owned = map[string]bool{}
		isolatedKillContainers[t] = owned
	}
	owned[id] = true
	isolatedKillMu.Unlock()
	t.Cleanup(func() {
		isolatedKillMu.Lock()
		delete(isolatedKillContainers, t)
		isolatedKillMu.Unlock()
	})
}

// declaredKillOwnership is the identities passed to registerIsolatedKillFixture
// and the container ids passed to registerIsolatedKillContainer. A RunRootPID
// or container id that only appears on a row is not owned.
func declaredKillOwnership(t *testing.T) isolatedKillOwnership {
	t.Helper()
	owned := isolatedKillOwnership{identities: map[int]isolatedProcessIdentity{}, containers: map[string]bool{}}
	isolatedKillMu.Lock()
	defer isolatedKillMu.Unlock()
	for _, fixture := range isolatedKillByTest[t] {
		for _, id := range fixture.identities {
			owned.identities[id.PID] = id
		}
	}
	for id := range isolatedKillContainers[t] {
		owned.containers[id] = true
	}
	return owned
}

func (owned isolatedKillOwnership) identityFor(pid int) (isolatedProcessIdentity, bool) {
	if pid <= 0 || owned.identities == nil {
		return isolatedProcessIdentity{}, false
	}
	id, ok := owned.identities[pid]
	if !ok || !id.usable() {
		return isolatedProcessIdentity{}, false
	}
	return id, true
}

func parseIsolatedBirth(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	if at, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return at.UTC()
	}
	if at, err := time.Parse(time.RFC3339, value); err == nil {
		return at.UTC()
	}
	return time.Time{}
}

func authorizeIsolatedPID(owned isolatedKillOwnership, pid int, claim time.Time) error {
	id, ok := owned.identityFor(pid)
	if !ok {
		return fmt.Errorf("%w: pid %d has no valid captured identity", errUnscopedKillScope, pid)
	}
	if claim.IsZero() {
		return fmt.Errorf("%w: pid %d has no birth claim; a bare pid is not ownership", errUnscopedKillScope, pid)
	}
	if !claim.Equal(id.StartedAt) {
		return fmt.Errorf("%w: pid %d birth claim does not match the captured identity", errUnscopedKillScope, pid)
	}
	return nil
}

// stampIsolatedKillClaims copies the captured birth claims onto a plan that
// named a pid without a claim, so a later real executor compares against the
// original occupant. A plan that already carries a different claim is left
// unchanged for the guard to refuse.
func stampIsolatedKillClaims(owned isolatedKillOwnership, targets []killer.Target, opts killer.Options) ([]killer.Target, killer.Options) {
	portsCopy := append([]ports.ListeningPort(nil), opts.Ports...)
	for i, row := range portsCopy {
		if id, ok := owned.identityFor(row.PID); ok && row.PID != 0 && row.StartedAt == "" {
			portsCopy[i].StartedAt = id.StartedAt.UTC().Format(time.RFC3339Nano)
		}
	}
	opts.Ports = portsCopy
	copied := append([]killer.Target(nil), targets...)
	for i, target := range copied {
		if id, ok := owned.identityFor(target.PID); ok && target.PID != 0 && target.StartedAt.IsZero() {
			copied[i].StartedAt = id.StartedAt
		}
	}
	return copied, opts
}

// invalidateIsolatedProcessIdentity marks a captured fixture ended. The same
// numeric pid must not authorize a later occupant.
func invalidateIsolatedProcessIdentity(owned isolatedKillOwnership, pid int) isolatedKillOwnership {
	if owned.identities == nil {
		return owned
	}
	copied := isolatedKillOwnership{identities: map[int]isolatedProcessIdentity{}, containers: map[string]bool{}}
	for k, v := range owned.identities {
		if k == pid {
			v.invalid = true
		}
		copied.identities[k] = v
	}
	for k, v := range owned.containers {
		copied.containers[k] = v
	}
	return copied
}

// guardIsolatedKillScope refuses a plan the production killer could widen past
// the captured fixture identities. It does not consult a live process table
// and does not call signalListenersForKill; contracts can invoke it in memory.
func guardIsolatedKillScope(owned isolatedKillOwnership, targets []killer.Target, opts killer.Options) error {
	if owned.identities == nil {
		owned.identities = map[int]isolatedProcessIdentity{}
	}
	if owned.containers == nil {
		owned.containers = map[string]bool{}
	}
	if opts.Ports == nil {
		return errUnscopedKillScan
	}
	implied := map[int]time.Time{}
	for _, row := range opts.Ports {
		if row.Type == ports.PortTypeDocker || row.DockerContainer != "" {
			if row.DockerContainer == "" || !owned.containers[row.DockerContainer] {
				return fmt.Errorf("%w: docker metadata %q is not owned by this test", errUnscopedKillScope, row.DockerContainer)
			}
		}
		birth := parseIsolatedBirth(row.StartedAt)
		if row.PID != 0 {
			if err := authorizeIsolatedPID(owned, row.PID, birth); err != nil {
				return err
			}
			implied[row.PID] = birth
		}
		if row.RunRootPID != 0 {
			rootBirth := birth
			if id, ok := owned.identityFor(row.RunRootPID); ok {
				// A row's StartedAt describes the listener; the root has its
				// own captured birth and must be authorized against that.
				rootBirth = id.StartedAt
				if err := authorizeIsolatedPID(owned, row.RunRootPID, rootBirth); err != nil {
					return err
				}
			} else if err := authorizeIsolatedPID(owned, row.RunRootPID, rootBirth); err != nil {
				return err
			}
			implied[row.RunRootPID] = rootBirth
		}
	}
	for _, target := range targets {
		if target.PID != 0 {
			claim := target.StartedAt.UTC()
			if target.StartedAt.IsZero() {
				claim = time.Time{}
			}
			if err := authorizeIsolatedPID(owned, target.PID, claim); err != nil {
				return err
			}
			implied[target.PID] = claim
		}
	}
	tree := opts.Tree
	for _, row := range opts.Ports {
		if row.RunRootPID != 0 {
			tree = true
		}
	}
	if tree {
		for pid, birth := range implied {
			if err := authorizeIsolatedPID(owned, pid, birth); err != nil {
				return fmt.Errorf("%w: tree or group member %d is not a captured fixture identity", errUnscopedKillScope, pid)
			}
		}
	}
	return nil
}

// capturePIDBirth reads the current occupant's start time through the same
// cross-platform observation the product uses (procfs on Linux, sysctl/ps on
// Darwin, process handles on Windows). It is a one-pid identity capture, not a
// descendant walk and not a live allowlist. A missing start time fails closed.
func capturePIDBirth(pid int) (time.Time, error) {
	if pid <= 0 {
		return time.Time{}, fmt.Errorf("pid %d is not a process", pid)
	}
	started, ok := ports.ProcessStartFreshContext(context.Background(), pid)
	if !ok || started.IsZero() {
		return time.Time{}, fmt.Errorf("pid %d has no observable start time", pid)
	}
	return started.UTC(), nil
}

func mustCapturePIDIdentity(t *testing.T, pid int) isolatedProcessIdentity {
	t.Helper()
	started, err := capturePIDBirth(pid)
	if err != nil {
		t.Fatalf("capturing birth for pid %d: %v", pid, err)
	}
	return claimedKillIdentity(pid, started)
}

// isolatedRegisteredSnapshot returns the rows this test registered, unchanged.
// It does not call ports.Enrich, groups.Attribute, or enrichIsolatedSnapshot.
// The pure-memory and fake-executor layer must use this snapshot only.
func isolatedRegisteredSnapshot(t *testing.T) []ports.ListeningPort {
	t.Helper()
	fixtures := fixturesFor(t)
	rows := make([]ports.ListeningPort, 0, len(fixtures))
	for _, f := range fixtures {
		rows = append(rows, f.row)
	}
	return rows
}

// enrichIsolatedSnapshot is the isolated-tier enrichment hook. Production CLI
// code is unaffected. The default applies ports.Enrich and groups.Attribute.
// The pure-memory path must not call this function; tests replace it to prove
// a path stayed registered-only, without running the real enrichers.
var enrichIsolatedSnapshot = enrichIsolatedSnapshotDefault

func enrichIsolatedSnapshotDefault(rows []ports.ListeningPort) []ports.ListeningPort {
	if len(rows) == 0 {
		return rows
	}
	ports.Enrich(rows)
	groups.Attribute(rows)
	return rows
}

func withEnrichIsolatedSnapshotHook(t *testing.T, hook func([]ports.ListeningPort) []ports.ListeningPort) {
	t.Helper()
	if hook == nil {
		t.Fatal("enrichment hook must be explicit; a nil hook is not a default restore")
	}
	prev := enrichIsolatedSnapshot
	enrichIsolatedSnapshot = hook
	t.Cleanup(func() { enrichIsolatedSnapshot = prev })
}

// isolatedEnrichedSnapshot is the already-isolated real-fixture tier: it first
// takes the registered rows, then applies enrichIsolatedSnapshot. Callers that
// need process-table or registry attribution must opt into this path. It is not
// part of the pure-memory layer.
func isolatedEnrichedSnapshot(t *testing.T) []ports.ListeningPort {
	t.Helper()
	return enrichIsolatedSnapshot(isolatedRegisteredSnapshot(t))
}

// isolatedListeningSnapshot is the real-fixture daemon tier: it takes the
// enriched rows and keeps only the listeners that still accept a connection.
// A fixture the test stopped must disappear from the next daemon scan the way a
// production scan loses a closed socket; a static row would make the daemon's
// "rescan straight after the kill" look stale when it is not.
func isolatedListeningSnapshot(t *testing.T) []ports.ListeningPort {
	t.Helper()
	rows := isolatedEnrichedSnapshot(t)
	kept := rows[:0]
	for _, row := range rows {
		if row.Port == 0 || listenerStillAccepts(row) {
			kept = append(kept, row)
		}
	}
	return kept
}

// listenerStillAccepts dials the listener the row claims. The dial is read-only
// and only ever reaches a fixture port; it is an observation, not a signal.
func listenerStillAccepts(row ports.ListeningPort) bool {
	host := row.BindAddress
	if host == "" {
		host = "127.0.0.1"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(row.Port)), 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func installIsolatedKillHooks(t *testing.T) {
	t.Helper()
	isolateKillScan(t, func() ([]ports.ListeningPort, error) {
		return isolatedRegisteredSnapshot(t), nil
	})
	isolateKillExecutor(t, allowlistedKillHere(t))
}

// useIsolatedEnrichedKillScan switches the direct kill scan to the isolated
// enriched tier. It must be called after installIsolatedKillHooks / fixture
// registration, and only from tests that already own real fixtures. Pure-memory
// contracts must not call it.
func useIsolatedEnrichedKillScan(t *testing.T) {
	t.Helper()
	isolateKillScan(t, func() ([]ports.ListeningPort, error) {
		return isolatedEnrichedSnapshot(t), nil
	})
}

func isolateKillScan(t *testing.T, scan func() ([]ports.ListeningPort, error)) {
	t.Helper()
	if scan == nil {
		t.Fatal("isolateKillScan requires a scan hook")
	}
	previous := scanForKill
	scanForKill = scan
	t.Cleanup(func() { scanForKill = previous })
}

func isolateKillExecutor(t *testing.T, exec killExecutor) {
	t.Helper()
	if exec == nil {
		t.Fatal("isolateKillExecutor requires an executor hook")
	}
	previous := killHere
	killHere = exec
	t.Cleanup(func() { killHere = previous })
}

// refuseKillExecutorIfReached is for contracts that must fail at selection:
// reaching the executor means the isolation or the contract is wrong, and no
// signal is sent.
func refuseKillExecutorIfReached(context.Context, []killer.Target, killer.Options, bool) ([]killer.Result, error) {
	return nil, errors.New("test isolation: selection should have failed before the executor; no signal sent")
}

func allowlistedKillHere(t *testing.T) killExecutor {
	t.Helper()
	return func(ctx context.Context, targets []killer.Target, opts killer.Options, dryRun bool) ([]killer.Result, error) {
		owned := declaredKillOwnership(t)
		targets, opts = stampIsolatedKillClaims(owned, targets, opts)
		if err := guardIsolatedKillScope(owned, targets, opts); err != nil {
			return nil, err
		}
		for _, target := range targets {
			if target.Port != 0 && !isolatedPortOwned(opts.Ports, target) {
				return nil, fmt.Errorf("test isolation: port %d is not in the isolated snapshot", target.Port)
			}
		}
		round := opts
		round.DryRun = dryRun
		// Non-nil Ports keeps killer.KillPorts from opening a second global scan.
		return signalListenersForKill(ctx, targets, round, dryRun)
	}
}

func isolatedPortOwned(snapshot []ports.ListeningPort, target killer.Target) bool {
	for _, row := range snapshot {
		if row.Port != target.Port {
			continue
		}
		if target.BindAddress == "" || row.BindAddress == target.BindAddress {
			return true
		}
	}
	return false
}

// killHeldProcess and killHeldProcessGroup are the only cleanup seams that may
// deliver a signal. They default to the held handle and, when the identity
// created a process group, that group's id. Tests replace them with recorders
// so contracts never send a real signal. There is no descendant-table hook.
var killHeldProcess = killHeldProcessDefault
var killHeldProcessGroup = killHeldProcessGroupDefault

func killHeldProcessDefault(id isolatedProcessIdentity) error {
	if id.Handle == nil {
		return fmt.Errorf("%w: no held handle", errUnscopedKillCleanup)
	}
	return id.Handle.Kill()
}

func withKillCleanupHooks(t *testing.T, proc, group func(isolatedProcessIdentity) error) {
	t.Helper()
	prevProc, prevGroup := killHeldProcess, killHeldProcessGroup
	killHeldProcess, killHeldProcessGroup = proc, group
	t.Cleanup(func() {
		killHeldProcess, killHeldProcessGroup = prevProc, prevGroup
	})
}

// cleanupHeldIdentity stops one captured fixture through its held handle (and
// its created process group, when marked). A claimed-only pid, an invalidated
// identity, or a mismatched handle is refused. The function never walks a
// process table and never signals a bare numeric pid.
func cleanupHeldIdentity(id isolatedProcessIdentity) error {
	if !id.usable() || !id.Held || id.Handle == nil || id.Handle.Pid != id.PID {
		return fmt.Errorf("%w: cleanup requires a still-valid held create-time handle, not a bare pid", errUnscopedKillCleanup)
	}
	// The handle can already be finished while the group it created still has
	// members (the root exited first). Attempt both and keep the first error:
	// returning on the handle error would leak the group it owns.
	var first error
	if err := killHeldProcess(id); err != nil {
		first = err
	}
	if id.ProcessGroup {
		if err := killHeldProcessGroup(id); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// killOwnedIdentities is the harness cleanup entry: the same identity policy
// as execution. Failures are returned and never fall back to a descendant walk.
func killOwnedIdentities(ids ...isolatedProcessIdentity) error {
	var first error
	for _, id := range ids {
		if err := cleanupHeldIdentity(id); err != nil && first == nil {
			first = err
		}
	}
	return first
}
