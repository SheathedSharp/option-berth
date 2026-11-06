package killer

import (
	"context"
	"errors"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// ---------------------------------------------------------------- harness ---

// signalCall is one delivery the fake machine recorded.
type signalCall struct {
	pid   int
	force bool
	group bool
}

// fakeClock advances only when the code under test sleeps, so an escalation
// test costs no wall-clock time and cannot flake on a slow machine.
type fakeClock struct {
	now   time.Time
	slept time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }

// Wait advances the fake clock instead of sleeping, and answers the same
// question the real one does: did the wait finish, or was it interrupted?
// A fake that always said "finished" would let a cancelled stop keep polling.
func (c *fakeClock) Wait(ctx context.Context, d time.Duration) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	c.now = c.now.Add(d)
	c.slept += d
	return true
}

// proc is one process the fake machine knows about. Presence in the map is what
// "alive" means: one record carries liveness, birth evidence and signal refusal
// together, so those three can never contradict each other the way three
// parallel maps can.
type proc struct {
	// born is when the pid started. Zero means alive but no birth evidence,
	// which is a different fact from a pid the machine does not know.
	born time.Time
	// group is the process group the pid leads. Zero means it leads none, so
	// the killer walks the tree instead of signalling the group.
	group int
	// refuses is the error the signal adapter returns for this pid.
	refuses error
}

// machine is a scriptable stand-in for the host: which processes exist, what
// one probe of a port establishes, and a log of every action taken against it.
type machine struct {
	table ProcessTable
	clock *fakeClock
	procs map[int]*proc
	// ports maps a port to what a probe of it establishes. A port the map does
	// not mention answers portClosed, the zero value.
	ports   map[int]portState
	signals []signalCall
	stopped []string
	// react runs after every delivery the adapter accepted, so a case can
	// describe how the machine answers a signal: a process that dies, one that
	// ignores it, one whose birth evidence disappears.
	react func(m *machine, c signalCall)
}

func newMachine(table ProcessTable) *machine {
	m := &machine{
		table: table,
		clock: &fakeClock{now: time.Unix(0, 0)},
		procs: map[int]*proc{},
		ports: map[int]portState{},
	}
	for pid := range table {
		m.procs[pid] = &proc{}
	}
	return m
}

// process returns the record for pid, creating it if the machine did not know
// the pid yet, so a test can describe one process in one expression.
func (m *machine) process(pid int) *proc {
	if p := m.procs[pid]; p != nil {
		return p
	}
	p := &proc{}
	m.procs[pid] = p
	return p
}

// exit removes a pid from the machine without going through a signal.
func (m *machine) exit(pid int) { delete(m.procs, pid) }

func (m *machine) alive(pid int) bool { return m.procs[pid] != nil }

func (m *machine) born(pid int) (time.Time, bool) {
	p := m.procs[pid]
	if p == nil || p.born.IsZero() {
		return time.Time{}, false
	}
	return p.born, true
}

func (m *machine) groupOf(pid int) (int, bool) {
	if p := m.procs[pid]; p != nil && p.group != 0 {
		return p.group, true
	}
	return 0, false
}

func (m *machine) probe(port int, _ string) portState { return m.ports[port] }

func (m *machine) dockerStop(name string) error {
	m.stopped = append(m.stopped, name)
	return nil
}

// deliver is the signal adapter: it records the attempt, then lets one process
// refuse it, then lets the case describe how the machine reacted.
func (m *machine) deliver(c signalCall) error {
	m.signals = append(m.signals, c)
	if p := m.procs[c.pid]; p != nil && p.refuses != nil {
		return p.refuses
	}
	if m.react != nil {
		m.react(m, c)
	}
	return nil
}

// engine builds the production engine with every machine-facing dependency
// pointed at this fake. Tree handling stays manual (nativeTree false) so the
// PPID walk is what these cases exercise.
func (m *machine) engine() *engine {
	return &engine{
		table:     m.table,
		clock:     m.clock,
		startTime: m.born,
		signalProc: func(pid int, force bool) error {
			return m.deliver(signalCall{pid: pid, force: force})
		},
		signalGrp: func(pgid int, force bool) error {
			return m.deliver(signalCall{pid: pgid, force: force, group: true})
		},
		signalTree: func(pid int, force bool) error {
			return m.deliver(signalCall{pid: pid, force: force})
		},
		groupOf:    m.groupOf,
		alive:      m.alive,
		probe:      m.probe,
		dockerStop: m.dockerStop,
	}
}

// stop runs one request against a given snapshot, so a case can inspect the
// unit plan without building a real scan.
func (m *machine) stop(snapshot []ports.ListeningPort, targets []Target, opts Options) []Result {
	return m.engine().kill(context.Background(), snapshot, targets, opts)
}

func (m *machine) signalledPIDs() []int {
	out := make([]int, 0, len(m.signals))
	for _, c := range m.signals {
		out = append(out, c.pid)
	}
	return out
}

// listener is a scanned port row with just the fields the killer reads.
func listener(port, pid int, opts ...func(*ports.ListeningPort)) ports.ListeningPort {
	lp := ports.ListeningPort{
		Port:        port,
		PID:         pid,
		BindAddress: "127.0.0.1",
		Process:     "node",
		Type:        ports.PortTypeUser,
	}
	for _, o := range opts {
		o(&lp)
	}
	return lp
}

func methods(results []Result) []state.KillMethod {
	out := make([]state.KillMethod, len(results))
	for i, r := range results {
		out[i] = r.Method
	}
	return out
}

func pidsOf(results []Result) []int {
	out := make([]int, len(results))
	for i, r := range results {
		out[i] = r.PID
	}
	return out
}

// ------------------------------------------------------------------ tests ---

func TestDryRunPlansTheTreeAndTouchesNothing(t *testing.T) {
	m := newMachine(npmTree())
	m.ports[3000] = portOpen

	results := m.stop([]ports.ListeningPort{listener(3000, 200)},
		[]Target{{Port: 3000}}, Options{Tree: true, DryRun: true})

	if got, want := pidsOf(results), []int{400, 401, 300, 200}; !reflect.DeepEqual(got, want) {
		t.Fatalf("planned pids = %v, want %v (children before parents)", got, want)
	}
	for _, r := range results {
		if r.Method != state.MethodSIGTERM || !r.OK {
			t.Errorf("row %+v: want a successful sigterm plan", r)
		}
		if r.Port != 3000 || r.BindAddress != "127.0.0.1" {
			t.Errorf("row %+v: every row carries the target port", r)
		}
	}
	if len(m.signals) != 0 || len(m.stopped) != 0 {
		t.Fatalf("dry run had side effects: signals=%v docker=%v", m.signals, m.stopped)
	}
	if m.clock.slept != 0 {
		t.Fatalf("dry run waited %v", m.clock.slept)
	}
}

func TestDryRunNamesEveryProcessInTheTree(t *testing.T) {
	m := newMachine(npmTree())

	results := m.stop([]ports.ListeningPort{listener(3000, 200)},
		[]Target{{Port: 3000}}, Options{Tree: true, DryRun: true})

	want := []string{"esbuild", "esbuild", "node", "node"}
	got := make([]string, len(results))
	for i, r := range results {
		got[i] = r.Name
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
}

func TestWithoutTreeOnlyTheListenerIsSignalled(t *testing.T) {
	m := newMachine(npmTree())

	results := m.stop([]ports.ListeningPort{listener(3000, 200)}, []Target{{Port: 3000}}, Options{})

	if got := m.signalledPIDs(); !reflect.DeepEqual(got, []int{200}) {
		t.Fatalf("signalled %v, want only the listener", got)
	}
	if len(results) != 1 || results[0].Method != state.MethodSIGTERM {
		t.Fatalf("results = %+v", results)
	}
}

func TestOverlappingProcessGroupTargetsSignalOnce(t *testing.T) {
	m := newMachine(ProcessTable{200: {PID: 200}})
	m.process(200).group = 200
	snapshot := []ports.ListeningPort{listener(3000, 200), listener(3001, 200)}

	results := m.stop(snapshot, []Target{{Port: 3000}, {Port: 3001}}, Options{})

	if got := m.signalledPIDs(); !reflect.DeepEqual(got, []int{200}) {
		t.Fatalf("signalled %v, want one process-group signal", got)
	}
	if len(results) != 1 || results[0].Port != 3000 || !results[0].OK {
		t.Fatalf("results = %+v, want one successful result for the first target", results)
	}
	if !m.signals[0].group {
		t.Fatalf("signals = %+v, want the group adapter", m.signals)
	}
}

func TestEscalatesToSIGKILLWhenThePortStaysOpen(t *testing.T) {
	m := newMachine(npmTree())
	// The listener ignores SIGTERM: nothing about it changes.
	m.ports[3000] = portOpen

	results := m.stop([]ports.ListeningPort{listener(3000, 200)},
		[]Target{{Port: 3000}}, Options{Grace: time.Second})

	want := []signalCall{{pid: 200}, {pid: 200, force: true}}
	if !reflect.DeepEqual(m.signals, want) {
		t.Fatalf("signals = %+v, want SIGTERM then SIGKILL", m.signals)
	}
	if m.clock.slept != time.Second {
		t.Fatalf("waited %v, want the full one second grace", m.clock.slept)
	}
	if got := methods(results); !reflect.DeepEqual(got, []state.KillMethod{state.MethodSIGKILL}) {
		t.Fatalf("methods = %v, want the row rewritten to sigkill", got)
	}
}

func TestNoEscalationWhenThePortCloses(t *testing.T) {
	m := newMachine(npmTree())
	m.ports[3000] = portOpen
	// A well-behaved process closes its listener and exits on the first SIGTERM.
	m.react = func(m *machine, c signalCall) {
		m.ports[3000] = portClosed
		m.exit(c.pid)
	}

	results := m.stop([]ports.ListeningPort{listener(3000, 200)},
		[]Target{{Port: 3000}}, Options{Grace: 5 * time.Second})

	if len(m.signals) != 1 || m.signals[0].force {
		t.Fatalf("signals = %+v, want a single SIGTERM", m.signals)
	}
	if m.clock.slept != 0 {
		t.Fatalf("waited %v, want no wait once the port is free", m.clock.slept)
	}
	if results[0].Method != state.MethodSIGTERM {
		t.Fatalf("method = %v, want sigterm", results[0].Method)
	}
}

func TestEscalatesWhenTheProbeCannotTell(t *testing.T) {
	m := newMachine(npmTree())
	// The listener that prompted this: it is still sitting on the port, its
	// accept queue is full, and it never answers. On the real machine this read
	// as "the port is free" — `ok: true`, method sigterm, one second, process
	// untouched — because "could not connect" was one fact instead of two.
	m.ports[3000] = portUnknown

	results := m.stop([]ports.ListeningPort{listener(3000, 200)},
		[]Target{{Port: 3000}}, Options{Grace: time.Second})

	want := []signalCall{{pid: 200}, {pid: 200, force: true}}
	if !reflect.DeepEqual(m.signals, want) {
		t.Fatalf("signals = %+v, want SIGTERM then SIGKILL", m.signals)
	}
	if m.clock.slept != time.Second {
		t.Fatalf("waited %v, want the full one second grace", m.clock.slept)
	}
	if got := methods(results); !reflect.DeepEqual(got, []state.KillMethod{state.MethodSIGKILL}) {
		t.Fatalf("methods = %v, want the row rewritten to sigkill", got)
	}
}

func TestNoEscalateOptionLeavesTheProcessAlone(t *testing.T) {
	m := newMachine(npmTree())
	m.ports[3000] = portOpen
	off := false

	results := m.stop([]ports.ListeningPort{listener(3000, 200)},
		[]Target{{Port: 3000}}, Options{Escalate: &off, Grace: time.Second})

	if len(m.signals) != 1 || m.signals[0].force {
		t.Fatalf("signals = %+v, want a single SIGTERM and no escalation", m.signals)
	}
	if m.clock.slept != 0 {
		t.Fatalf("waited %v with --no-escalate", m.clock.slept)
	}
	if results[0].Method != state.MethodSIGTERM {
		t.Fatalf("method = %v", results[0].Method)
	}
}

func TestForceSkipsStraightToSIGKILL(t *testing.T) {
	m := newMachine(npmTree())
	m.ports[3000] = portOpen

	results := m.stop([]ports.ListeningPort{listener(3000, 200)},
		[]Target{{Port: 3000}}, Options{Force: true})

	if len(m.signals) != 1 || !m.signals[0].force {
		t.Fatalf("signals = %+v, want one SIGKILL", m.signals)
	}
	if m.clock.slept != 0 {
		t.Fatalf("--force waited %v", m.clock.slept)
	}
	if results[0].Method != state.MethodSIGKILL {
		t.Fatalf("method = %v", results[0].Method)
	}
}

func TestEscalationOnlyHardKillsWhatIsStillAlive(t *testing.T) {
	m := newMachine(npmTree())
	m.ports[3000] = portOpen // the parent hangs on to the socket
	// The workers exit on SIGTERM; the parent does not.
	m.react = func(m *machine, c signalCall) {
		if c.pid != 200 {
			m.exit(c.pid)
		}
	}

	results := m.stop([]ports.ListeningPort{listener(3000, 200)},
		[]Target{{Port: 3000}}, Options{Tree: true, Grace: time.Second})

	var hard []int
	for _, c := range m.signals {
		if c.force {
			hard = append(hard, c.pid)
		}
	}
	if !reflect.DeepEqual(hard, []int{200}) {
		t.Fatalf("SIGKILLed %v, want only the process still alive", hard)
	}
	if got := methods(results); !reflect.DeepEqual(got, []state.KillMethod{
		state.MethodSIGTERM, state.MethodSIGTERM, state.MethodSIGTERM, state.MethodSIGKILL,
	}) {
		t.Fatalf("methods = %v", got)
	}
}

func TestDockerContainersAreStoppedNotSignalled(t *testing.T) {
	m := newMachine(npmTree())
	snapshot := []ports.ListeningPort{listener(5432, 900, func(lp *ports.ListeningPort) {
		lp.Type = ports.PortTypeDocker
		lp.DockerContainer = "pg"
		lp.DockerComposeService = "db"
	})}

	results := m.stop(snapshot, []Target{{Port: 5432}}, Options{Tree: true, Force: true})

	if len(m.signals) != 0 {
		t.Fatalf("signalled a container: %+v", m.signals)
	}
	if !reflect.DeepEqual(m.stopped, []string{"pg"}) {
		t.Fatalf("stopped = %v, want [pg]", m.stopped)
	}
	if results[0].Method != state.MethodDockerStop || results[0].Name != "db" {
		t.Fatalf("row = %+v", results[0])
	}
}

func TestRunAttributedListenerKillsTheWholeRun(t *testing.T) {
	m := newMachine(npmTree())
	// vite listens, but option-berth started the shell at pid 100.
	snapshot := []ports.ListeningPort{listener(3000, 300, func(lp *ports.ListeningPort) {
		lp.RunRootPID = 100
		lp.RunID = "abc123"
		lp.Tag = "web"
	})}

	// No --tree: run attribution implies the whole tree anyway.
	m.stop(snapshot, []Target{{Port: 3000}}, Options{})

	if got, want := m.signalledPIDs(), []int{400, 401, 300, 200, 201, 100}; !reflect.DeepEqual(got, want) {
		t.Fatalf("signalled %v, want the whole run tree children-first %v", got, want)
	}
}

func TestRunWithItsOwnProcessGroupIsSignalledAsAGroup(t *testing.T) {
	m := newMachine(npmTree())
	m.process(100).group = 100 // spawned with Setpgid
	snapshot := []ports.ListeningPort{listener(3000, 300, func(lp *ports.ListeningPort) {
		lp.RunRootPID = 100
	})}

	results := m.stop(snapshot, []Target{{Port: 3000}}, Options{})

	want := []signalCall{{pid: 100, group: true}}
	if !reflect.DeepEqual(m.signals, want) {
		t.Fatalf("signals = %+v, want one process-group signal", m.signals)
	}
	if len(results) != 1 || results[0].PID != 100 {
		t.Fatalf("results = %+v", results)
	}
}

func TestRunIDTargetExpandsToEveryPortOfTheRun(t *testing.T) {
	m := newMachine(npmTree())
	snapshot := []ports.ListeningPort{
		listener(3000, 300, func(lp *ports.ListeningPort) { lp.RunID = "abc"; lp.RunRootPID = 300 }),
		listener(24678, 300, func(lp *ports.ListeningPort) { lp.RunID = "abc"; lp.RunRootPID = 300 }),
		listener(5173, 999),
	}

	results := m.stop(snapshot, []Target{{RunID: "abc"}}, Options{})

	// Both ports belong to the same tree; the shared pids are signalled once.
	if got := m.signalledPIDs(); !reflect.DeepEqual(got, []int{400, 401, 300}) {
		t.Fatalf("signalled %v", got)
	}
	if len(results) != 3 {
		t.Fatalf("results = %+v, want three rows (one per signalled process)", results)
	}
}

func TestOverlappingTargetsSignalEachProcessOnce(t *testing.T) {
	m := newMachine(npmTree())
	// 3000 is npm's tree; 3001 is vite inside that same tree.
	snapshot := []ports.ListeningPort{listener(3000, 200), listener(3001, 300)}

	m.stop(snapshot, []Target{{Port: 3000}, {Port: 3001}}, Options{Tree: true})

	if got, want := m.signalledPIDs(), []int{400, 401, 300, 200}; !reflect.DeepEqual(got, want) {
		t.Fatalf("signalled %v, want %v: the inner tree first, no pid twice", got, want)
	}
}

func TestAmbiguousPortReportsTheBindAddresses(t *testing.T) {
	m := newMachine(npmTree())
	snapshot := []ports.ListeningPort{
		listener(3000, 200),
		listener(3000, 201, func(lp *ports.ListeningPort) { lp.BindAddress = "::1" }),
	}

	results := m.stop(snapshot, []Target{{Port: 3000}}, Options{})

	if len(results) != 1 || results[0].OK || results[0].Method != state.MethodNone {
		t.Fatalf("results = %+v, want one failed row", results)
	}
	if len(m.signals) != 0 {
		t.Fatalf("signalled despite ambiguity: %+v", m.signals)
	}

	// With --ip the same call resolves.
	m2 := newMachine(npmTree())
	results = m2.stop(snapshot, []Target{{Port: 3000, BindAddress: "::1"}}, Options{})
	if len(results) != 1 || !results[0].OK || results[0].PID != 201 {
		t.Fatalf("results = %+v", results)
	}
}

func TestUnknownPortIsANotFoundRow(t *testing.T) {
	m := newMachine(npmTree())

	results := m.stop(nil, []Target{{Port: 9999}}, Options{})

	if len(results) != 1 || results[0].OK || results[0].Port != 9999 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Error == "" {
		t.Fatal("a failed row must carry its error")
	}
}

func TestHiddenListenerAsksForSudo(t *testing.T) {
	m := newMachine(npmTree())

	results := m.stop([]ports.ListeningPort{listener(3000, 0)}, []Target{{Port: 3000}}, Options{})

	if len(results) != 1 || results[0].OK {
		t.Fatalf("results = %+v", results)
	}
	if len(m.signals) != 0 {
		t.Fatalf("signalled PID 0: %+v", m.signals)
	}
}

func TestSignalFailureIsReportedPerRow(t *testing.T) {
	m := newMachine(npmTree())
	denied := permissionErr(200, errors.New("operation not permitted"))
	m.process(200).refuses = denied

	results := m.stop([]ports.ListeningPort{listener(3000, 200)}, []Target{{Port: 3000}}, Options{})

	if len(results) != 1 || results[0].OK {
		t.Fatalf("results = %+v", results)
	}
	if Code(denied) != CodePermissionDenied || Hint(denied) != "re-run with sudo" {
		t.Fatalf("permission errors must map to the contract code and the sudo hint")
	}
	if results[0].Code != CodePermissionDenied {
		t.Fatalf("row = %+v, want the failure's contract code", results[0])
	}
}

func TestPIDTargetWithoutAListeningPort(t *testing.T) {
	m := newMachine(npmTree())
	off := false

	results := m.stop(nil, []Target{{PID: 300}}, Options{Tree: true, Escalate: &off})

	if got := m.signalledPIDs(); !reflect.DeepEqual(got, []int{400, 401, 300}) {
		t.Fatalf("signalled %v", got)
	}
	if len(results) != 3 || results[0].Port != 0 {
		t.Fatalf("results = %+v, want portless rows", results)
	}
}

func TestPIDTargetEscalatesOnTheProcess(t *testing.T) {
	m := newMachine(npmTree()) // every pid stays alive: nothing reacts to SIGTERM

	m.stop(nil, []Target{{PID: 400}}, Options{Grace: time.Second})

	want := []signalCall{{pid: 400}, {pid: 400, force: true}}
	if !reflect.DeepEqual(m.signals, want) {
		t.Fatalf("signals = %+v, want SIGTERM then SIGKILL", m.signals)
	}
}

func TestEmptyTargetIsRejected(t *testing.T) {
	m := newMachine(npmTree())

	results := m.stop(nil, []Target{{}}, Options{})

	if len(results) != 1 || results[0].OK {
		t.Fatalf("results = %+v", results)
	}
}

func TestCancelledContextStopsBeforeEscalation(t *testing.T) {
	m := newMachine(npmTree())
	m.ports[3000] = portOpen
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel only after the initial accepted signal. A pre-canceled request
	// correctly sends no signal and is covered by the admission tests.
	m.engine().kill(ctx, []ports.ListeningPort{listener(3000, 200)}, []Target{{Port: 3000}},
		Options{Grace: time.Minute, OnSignal: func(int) { cancel() }})

	if len(m.signals) != 1 || m.signals[0].force {
		t.Fatalf("signals = %+v, want the SIGTERM only", m.signals)
	}
}

func TestOptionDefaults(t *testing.T) {
	var o Options
	if o.grace() != DefaultGrace {
		t.Errorf("grace = %v, want %v", o.grace(), DefaultGrace)
	}
	if !o.escalating() {
		t.Error("the zero Options must escalate")
	}
	if (Options{Force: true}).escalating() {
		t.Error("--force is already the strongest signal; nothing to escalate to")
	}
}

// TestProbeTellsTheThreeThingsApart is the decision probePort makes, with the
// dialer handed to it so all three outcomes are reachable. The middle one is
// the whole reason the type is not a bool: a port whose listener has stopped
// accepting answers with a timeout, and that is not the same fact as a refusal.
//
// The refusal case is deliberately any old non-timeout error rather than a
// named syscall: what the code asks is only whether the dial timed out, and
// pinning `syscall.ECONNREFUSED` here would pin the test to one platform's
// spelling of it.
func TestProbeTellsTheThreeThingsApart(t *testing.T) {
	refused := errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
	cases := []struct {
		name string
		dial func(network, address string, timeout time.Duration) (net.Conn, error)
		want portState
	}{
		{
			name: "answered",
			dial: func(string, string, time.Duration) (net.Conn, error) {
				conn, other := net.Pipe()
				defer other.Close()
				return conn, nil
			},
			want: portOpen,
		},
		{
			name: "refused",
			dial: func(string, string, time.Duration) (net.Conn, error) { return nil, refused },
			want: portClosed,
		},
		{
			name: "silent",
			dial: func(string, string, time.Duration) (net.Conn, error) {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
			},
			want: portUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeWith(tc.dial, 1, "127.0.0.1"); got != tc.want {
				t.Errorf("probeWith = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestProbeOnARealPort covers the two outcomes a machine can be asked for on
// demand: a listener that answers, and a port nothing is on. The timeout
// outcome needs a listener that has stopped accepting, which no test can
// arrange portably — the injected-dialer case above is where it is pinned.
func TestProbeOnARealPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	if got := probePort(port, "127.0.0.1"); got != portOpen {
		t.Errorf("probe of a live listener = %v, want portOpen", got)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("closing the listener: %v", err)
	}
	if got := probePort(port, "127.0.0.1"); got != portClosed {
		t.Errorf("probe of a closed port = %v, want portClosed", got)
	}
}

// TestRecycledPIDTargetIsRefused: a stop that names a run hands the killer the
// run's recorded start. If the pid now belongs to a newer process, the run is
// already gone — the target is refused at the last moment before signalling
// instead of killing something this tool never started. A start within
// tolerance, and no answer at all, both keep the kill going.
func TestRecycledPIDTargetIsRefused(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	target := []Target{{PID: 777, StartedAt: base}}

	recycled := newMachine(ProcessTable{})
	recycled.process(777).born = base.Add(time.Hour)
	results := recycled.engine().kill(context.Background(), nil, target, Options{})
	if len(recycled.signals) != 0 {
		t.Fatalf("signalled a recycled pid: %+v", recycled.signals)
	}
	if len(results) != 1 || results[0].OK || results[0].Code != CodeNotFound {
		t.Fatalf("result = %+v, want a refused target", results)
	}

	// Started within the tolerance: the same process, not a recycled pid.
	same := newMachine(ProcessTable{})
	same.process(777).born = base.Add(time.Second)
	same.react = func(m *machine, c signalCall) { m.exit(c.pid) }
	same.engine().kill(context.Background(), nil, target, Options{})
	if len(same.signals) != 1 || same.signals[0].pid != 777 {
		t.Fatalf("signals = %+v, want the recorded process signalled", same.signals)
	}

	// No birth evidence at all is not evidence of replacement.
	unknown := newMachine(ProcessTable{})
	unknown.process(777)
	unknown.react = func(m *machine, c signalCall) { m.exit(c.pid) }
	unknown.engine().kill(context.Background(), nil, target, Options{})
	if len(unknown.signals) != 1 || unknown.signals[0].pid != 777 {
		t.Fatalf("signals = %+v, want an unknown start time to keep the kill", unknown.signals)
	}
}
