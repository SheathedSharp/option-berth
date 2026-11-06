package killer

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func TestPIDClaimSurvivesListenerResolution(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := newMachine(ProcessTable{777: {PID: 777}})
	m.process(777).born = base.Add(time.Hour)

	rows := m.engine().kill(context.Background(), []ports.ListeningPort{listener(3000, 777)},
		[]Target{{PID: 777, StartedAt: base}}, Options{Force: true})

	if len(m.signals) != 0 || len(rows) != 1 || rows[0].OK || rows[0].Code != CodeNotFound || rows[0].Method != state.MethodNone {
		t.Fatalf("stale run claim matched listener: signals=%v rows=%+v", m.signals, rows)
	}
}

func TestPIDRevalidatedBetweenPlanningAndSignal(t *testing.T) {
	m := newMachine(ProcessTable{42: {PID: 42}})
	e := m.engine()
	reads := 0
	// Every read reports a newer birth than the last: the pid the caller named
	// is not the pid that is there by the time the signal would go out.
	e.startTime = func(int) (time.Time, bool) {
		reads++
		return time.Unix(int64(reads)*3600, 0), true
	}

	rows := e.kill(context.Background(), nil, []Target{{PID: 42}}, Options{Force: true})

	if len(m.signals) != 0 || len(rows) != 1 || rows[0].OK || rows[0].Method != state.MethodNone {
		t.Fatalf("plan-time evidence was not checked again: signals=%v rows=%+v", m.signals, rows)
	}
}

func TestEscalationRejectsRecycledPIDAndGroup(t *testing.T) {
	for _, group := range []bool{false, true} {
		m := newMachine(ProcessTable{42: {PID: 42}})
		m.process(42).born = time.Unix(1000, 0)
		m.ports[3000] = portOpen
		if group {
			m.process(42).group = 42
		}
		// The pid is recycled between the accepted SIGTERM and the escalation.
		m.react = func(m *machine, c signalCall) { m.process(c.pid).born = time.Unix(2000, 0) }

		rows := m.engine().kill(context.Background(), []ports.ListeningPort{listener(3000, 42)},
			[]Target{{Port: 3000}}, Options{Grace: time.Second})

		if len(m.signals) != 1 || m.signals[0].force || len(rows) != 1 || rows[0].OK ||
			rows[0].Code != CodeNotFound || rows[0].Method != state.MethodSIGTERM {
			t.Fatalf("group=%v: replacement received escalation: signals=%v rows=%+v", group, m.signals, rows)
		}
	}
}

func TestEscalationRefusesLostKnownBirth(t *testing.T) {
	m := newMachine(ProcessTable{42: {PID: 42}})
	m.process(42).born = time.Unix(1000, 0)
	m.ports[3000] = portOpen
	// The process is still there but its birth evidence is gone: unknown must
	// not become approval.
	m.react = func(m *machine, c signalCall) {
		if p := m.procs[c.pid]; p != nil {
			p.born = time.Time{}
		}
	}

	rows := m.engine().kill(context.Background(), []ports.ListeningPort{listener(3000, 42)},
		[]Target{{Port: 3000}}, Options{Grace: time.Second})

	if len(m.signals) != 1 || rows[0].OK || rows[0].Code != CodePermissionDenied || rows[0].Method != state.MethodSIGTERM {
		t.Fatalf("missing evidence became identity approval: signals=%v rows=%+v", m.signals, rows)
	}
}

func TestTreeIdentityChecksDoNotBlockAfterChildExit(t *testing.T) {
	m := newMachine(ProcessTable{10: {PID: 10}, 20: {PID: 20, PPID: 10}, 21: {PID: 21, PPID: 10}})
	for pid := range m.table {
		m.process(pid).born = time.Unix(int64(pid)*10, 0)
	}
	// A child that exits on our signal is gone, evidence and all. That must not
	// stop the walk from finishing the rest of the tree.
	m.react = func(m *machine, c signalCall) { m.exit(c.pid) }
	snapshot := []ports.ListeningPort{listener(3000, 20, func(p *ports.ListeningPort) { p.RunRootPID = 10 })}

	rows := m.engine().kill(context.Background(), snapshot, []Target{{Port: 3000}}, Options{Force: true})

	if !reflect.DeepEqual(m.signalledPIDs(), []int{20, 21, 10}) {
		t.Fatalf("stopped after listener exit: %v", m.signals)
	}
	for _, row := range rows {
		if !row.OK {
			t.Fatalf("row=%+v", row)
		}
	}
}

func TestCancellationStopsLaterSignalsAndDocker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newMachine(ProcessTable{42: {PID: 42}})
	m.react = func(*machine, signalCall) { cancel() }
	snapshot := []ports.ListeningPort{
		listener(3000, 42),
		listener(4000, 90, func(p *ports.ListeningPort) { p.Type = ports.PortTypeDocker; p.DockerContainer = "demo" }),
	}

	rows := m.engine().kill(ctx, snapshot, []Target{{Port: 3000}, {Port: 4000}}, Options{Force: true})

	if len(m.signals) != 1 || len(m.stopped) != 0 || len(rows) != 2 || rows[1].OK || rows[1].Method != state.MethodNone {
		t.Fatalf("cancelled operation continued: signals=%v docker=%v rows=%+v", m.signals, m.stopped, rows)
	}
}

type cancelSleepClock struct {
	fakeClock
	cancel context.CancelFunc
}

func (c *cancelSleepClock) Sleep(d time.Duration) { c.fakeClock.Sleep(d); c.cancel() }

func TestCancellationAtGraceBoundaryNeverEscalates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newMachine(ProcessTable{42: {PID: 42}})
	m.ports[3000] = portOpen
	e := m.engine()
	e.clock = &cancelSleepClock{fakeClock: *m.clock, cancel: cancel}

	rows := e.kill(ctx, []ports.ListeningPort{listener(3000, 42)}, []Target{{Port: 3000}}, Options{Grace: pollInterval})

	if len(m.signals) != 1 || m.signals[0].force || rows[0].OK || rows[0].Method != state.MethodSIGTERM {
		t.Fatalf("cancelled grace performed escalation or claimed completion: %v %+v", m.signals, rows)
	}
}

func TestInheritedNativeTreeRemainsATreeDuringEscalation(t *testing.T) {
	m := newMachine(ProcessTable{42: {PID: 42}, 43: {PID: 43, PPID: 42}})
	m.ports[3000] = portOpen
	e := m.engine()
	e.nativeTree = true
	var trees []bool
	e.signalTree = func(_ int, force bool) error { trees = append(trees, force); return nil }
	snapshot := []ports.ListeningPort{listener(3000, 43, func(p *ports.ListeningPort) { p.RunRootPID = 42 })}

	e.kill(context.Background(), snapshot, []Target{{Port: 3000}}, Options{Grace: time.Second})

	if len(m.signals) != 0 || !reflect.DeepEqual(trees, []bool{false, true}) {
		t.Fatalf("inherited tree was reduced to a process: process=%v tree=%v", m.signals, trees)
	}
}

func TestFailedInitialSignalIsNotEscalated(t *testing.T) {
	m := newMachine(ProcessTable{42: {PID: 42}})
	m.ports[3000] = portOpen
	m.process(42).refuses = permissionErr(42, errors.New("denied"))

	rows := m.engine().kill(context.Background(), []ports.ListeningPort{listener(3000, 42)},
		[]Target{{Port: 3000}}, Options{})

	if len(m.signals) != 1 || rows[0].OK || m.clock.slept != 0 {
		t.Fatalf("failed action was retried: %v %+v", m.signals, rows)
	}
}

func TestCancelledDirectKillNeverCollects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rows := KillPorts(ctx, []Target{{Port: 3000}}, Options{})

	if len(rows) != 1 || rows[0].OK || rows[0].Method != state.MethodNone {
		t.Fatalf("rows=%+v", rows)
	}
	// Both fresh readers must return before any platform IO on this input.
	if _, ok := ports.ProcessStartFreshContext(ctx, 42); ok {
		t.Fatal("cancelled reader returned identity")
	}
}

func TestManualTreeRechecksTheListenerItActuallySignals(t *testing.T) {
	m := newMachine(ProcessTable{10: {PID: 10}, 20: {PID: 20, PPID: 10}})
	e := m.engine()
	reads := map[int]int{}
	// The listener (20) is recycled after the plan captured it; the tree root
	// (10) is not.
	e.startTime = func(pid int) (time.Time, bool) {
		reads[pid]++
		at := time.Unix(1000, 0)
		if pid == 20 && reads[pid] > 1 {
			at = at.Add(time.Hour)
		}
		return at, true
	}
	snapshot := []ports.ListeningPort{listener(3000, 20, func(p *ports.ListeningPort) { p.RunRootPID = 10 })}

	rows := e.kill(context.Background(), snapshot, []Target{{Port: 3000}}, Options{Force: true})

	for _, c := range m.signals {
		if c.pid == 20 {
			t.Fatal("recycled tree listener was signalled")
		}
	}
	if len(rows) != 2 || rows[0].OK || rows[0].Code != CodeNotFound {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestControlBudgetCannotOverflow(t *testing.T) {
	const largest = time.Duration(1<<63 - 1)
	if controlBudget(Options{Grace: largest}) != largest {
		t.Fatal("grace overflowed")
	}
	if controlBudget(Options{Force: true, Grace: largest}) != 15*time.Second {
		t.Fatal("force counted grace")
	}
	if controlBudget(Options{}) != 15*time.Second+DefaultGrace {
		t.Fatal("default budget changed")
	}
}

func TestPIDBoundsDoNotReachSignalAdapters(t *testing.T) {
	if validSignalPID(0) || validSignalPID(-1) {
		t.Fatal("non-positive PID accepted")
	}
	if !validSignalPID(42) {
		t.Fatal("ordinary PID rejected")
	}
	// Conversion via a variable keeps this definition portable to 32-bit Go.
	huge := uint64(1<<33) + 42
	if uint64(int(huge)) != huge {
		return
	}
	m := newMachine(ProcessTable{})
	e := m.engine()
	e.alive = func(int) bool { t.Fatal("invalid PID reached a system adapter"); return true }

	rows := e.kill(context.Background(), nil, []Target{{PID: int(huge)}}, Options{Force: true})

	if len(rows) != 1 || rows[0].OK || rows[0].Code != CodeInvalidSelector {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestFirstKnownSignalBirthPersistsIntoEscalation(t *testing.T) {
	m := newMachine(ProcessTable{42: {PID: 42}})
	m.ports[3000] = portOpen
	e := m.engine()
	reads := 0
	current := time.Unix(1000, 0)
	// The first read (capture) has no answer; the signal boundary is where the
	// first positive evidence appears, and later phases must not forget it and
	// then accept a different birth as the same process.
	e.startTime = func(int) (time.Time, bool) {
		reads++
		if reads == 1 {
			return time.Time{}, false
		}
		return current, true
	}
	m.react = func(*machine, signalCall) { current = current.Add(time.Hour) }

	rows := e.kill(context.Background(), []ports.ListeningPort{listener(3000, 42)},
		[]Target{{Port: 3000}}, Options{Grace: time.Second})

	if len(m.signals) != 1 || rows[0].OK || rows[0].Code != CodeNotFound {
		t.Fatalf("signals=%v rows=%+v", m.signals, rows)
	}
}

// TestEscalationRefusesRemainingPidsWhenTheRequestIsCancelled pins the second
// cancellation boundary: the one inside the hard-kill loop. Cancelling after
// the grace period has already expired, while some of a tree is still standing,
// must not let the rest of the tree be SIGKILLed — the request was withdrawn.
func TestEscalationRefusesRemainingPidsWhenTheRequestIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newMachine(ProcessTable{10: {PID: 10}, 20: {PID: 20, PPID: 10}, 21: {PID: 21, PPID: 10}})
	m.ports[3000] = portOpen // the port stays held, so the grace runs out
	snapshot := []ports.ListeningPort{listener(3000, 20, func(p *ports.ListeningPort) { p.RunRootPID = 10 })}
	// Nothing exits, so the whole tree is still pending when the grace expires.
	// The first hard kill withdraws the request; the rest must be refused.
	m.react = func(_ *machine, c signalCall) {
		if c.force {
			cancel()
		}
	}

	rows := m.engine().kill(ctx, snapshot, []Target{{Port: 3000}}, Options{Grace: time.Millisecond})

	// The one delivery already in flight when the request was withdrawn is
	// reported as what it was. What must not happen is the rest of the tree.
	var forced []int
	for _, c := range m.signals {
		if c.force {
			forced = append(forced, c.pid)
		}
	}
	if !reflect.DeepEqual(forced, []int{20}) {
		t.Fatalf("kept killing after cancellation: %+v", m.signals)
	}
	for _, row := range rows {
		if row.PID != 20 && row.OK {
			t.Fatalf("a stop the request no longer wanted was reported successful: %+v", row)
		}
	}
}

// TestEscalationDoesNotRetryAStopThatFailed pins that SIGKILL is escalation, not
// retry: a signal the OS refused once is refused again, so sending the harder
// one to the same pid would be an action nobody asked for.
func TestEscalationDoesNotRetryAStopThatFailed(t *testing.T) {
	m := newMachine(ProcessTable{10: {PID: 10}, 20: {PID: 20, PPID: 10}, 21: {PID: 21, PPID: 10}})
	m.process(21).refuses = permissionErr(21, errors.New("operation not permitted"))
	m.ports[3000] = portOpen // the port never closes, so escalation happens
	snapshot := []ports.ListeningPort{listener(3000, 20, func(p *ports.ListeningPort) { p.RunRootPID = 10 })}

	rows := m.engine().kill(context.Background(), snapshot, []Target{{Port: 3000}}, Options{Grace: time.Second})

	var forced []int
	for _, c := range m.signals {
		if c.force {
			forced = append(forced, c.pid)
		}
	}
	if !reflect.DeepEqual(forced, []int{20, 10}) {
		t.Fatalf("escalated %v, want only the stops that had been accepted", forced)
	}
	for _, row := range rows {
		if row.PID == 21 && (row.OK || row.Code != CodePermissionDenied) {
			t.Fatalf("refused stop was reported as retried: %+v", row)
		}
	}
}

// TestCancelledEscalationTouchesNoFurtherProcess pins the last cancellation
// boundary: once the request is withdrawn, the escalation loop must not ask the
// OS about the processes it has left. A withdrawn stop does not get to keep
// probing the machine.
func TestCancelledEscalationTouchesNoFurtherProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newMachine(ProcessTable{10: {PID: 10}, 20: {PID: 20, PPID: 10}, 21: {PID: 21, PPID: 10}})
	m.ports[3000] = portOpen
	snapshot := []ports.ListeningPort{listener(3000, 20, func(p *ports.ListeningPort) { p.RunRootPID = 10 })}

	e := m.engine()
	withdrawn := false
	alive := e.alive
	e.alive = func(pid int) bool {
		if withdrawn {
			t.Fatalf("probed PID %d after the request was withdrawn", pid)
		}
		return alive(pid)
	}
	m.react = func(_ *machine, c signalCall) {
		if c.force {
			withdrawn = true
			cancel()
		}
	}

	e.kill(ctx, snapshot, []Target{{Port: 3000}}, Options{Grace: time.Millisecond})
}
