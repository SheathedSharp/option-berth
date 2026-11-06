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

// cancelSleepClock withdraws the request exactly when the grace period would
// otherwise be waited out.
type cancelSleepClock struct {
	fakeClock
	cancel context.CancelFunc
}

func (c *cancelSleepClock) Wait(ctx context.Context, d time.Duration) bool {
	c.fakeClock.Wait(ctx, d)
	c.cancel()
	return false
}

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

// interruptibleClock is a clock whose only unusual property is the one the
// Clock contract states: its wait ends when the request does.
type interruptibleClock struct {
	fakeClock
	// waits counts completed waits; interrupted counts waits a cancellation cut
	// short. They must never both advance for the same wait.
	waits       int
	interrupted int
}

func (c *interruptibleClock) Wait(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		c.interrupted++
		return false
	}
	c.fakeClock.Wait(ctx, d)
	c.waits++
	return true
}

// TestCancelledGraceDoesNotWaitOutThePeriod pins the Clock contract from the
// caller's side: when the request is withdrawn during the poll, the escalation
// loop must stop then and there. A wait that reports "interrupted" and is
// ignored would keep an abandoned stop polling for the rest of its grace.
func TestCancelledGraceDoesNotWaitOutThePeriod(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newMachine(ProcessTable{42: {PID: 42}})
	m.ports[3000] = portOpen
	e := m.engine()
	clock := &interruptibleClock{fakeClock: *m.clock}
	e.clock = clock
	// Withdraw the request the moment the first signal is accepted, which is
	// before the loop has decided to poll.
	m.react = func(*machine, signalCall) { cancel() }

	rows := e.kill(ctx, []ports.ListeningPort{listener(3000, 42)}, []Target{{Port: 3000}},
		Options{Grace: time.Hour})

	if clock.waits != 0 {
		t.Fatalf("waited %d times for a request that was already withdrawn", clock.waits)
	}
	if len(m.signals) != 1 || m.signals[0].force {
		t.Fatalf("signals = %+v, want the accepted SIGTERM and no escalation", m.signals)
	}
	if rows[0].OK {
		t.Fatalf("row = %+v, want the interrupted stop reported as unfinished", rows[0])
	}
}

// withdrawingClock withdraws the request during a wait, the way a person
// pressing Ctrl-C lands: the stop is asleep when it has to give up.
type withdrawingClock struct {
	fakeClock
	cancel context.CancelFunc
}

func (c *withdrawingClock) Wait(ctx context.Context, d time.Duration) bool {
	c.cancel()
	return false
}

// TestWithdrawnWaitTouchesNoFurtherProcess pins what an interrupted wait saves.
// The escalation loop checks cancellation at its top anyway, so the observable
// difference is one round of machine observation: a stop revoked during a poll
// must not go on to probe the port or the processes it was about to escalate.
func TestWithdrawnWaitTouchesNoFurtherProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newMachine(ProcessTable{42: {PID: 42}})
	m.ports[3000] = portOpen
	e := m.engine()
	e.clock = &withdrawingClock{fakeClock: *m.clock, cancel: cancel}

	probes, aliveChecks := 0, 0
	probe := e.probe
	e.probe = func(port int, bind string) portState { probes++; return probe(port, bind) }
	alive := e.alive
	e.alive = func(pid int) bool { aliveChecks++; return alive(pid) }

	e.kill(ctx, []ports.ListeningPort{listener(3000, 42)}, []Target{{Port: 3000}}, Options{Grace: time.Hour})

	// Before the wait: the escalation loop's own stillUp. After it: nothing.
	if probes != 1 || aliveChecks != 0 {
		t.Fatalf("probed after the request was withdrawn: probes=%d alive=%d", probes, aliveChecks)
	}
}

// TestEscalationStopsWhenTheProbeItselfRevealsCancellation pins the two
// cancellation checks inside the escalation loop. The request can be withdrawn
// at any point — including while the loop is asking the machine about the port
// — and the loop must stop rather than treat the result it just got as a
// reason to keep going. Only a probe can cancel at this instant, so a case
// built on a clock cannot reach it.
func TestEscalationStopsWhenTheProbeItselfRevealsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newMachine(ProcessTable{42: {PID: 42}})
	m.ports[3000] = portOpen
	e := m.engine()

	probes := 0
	probe := e.probe
	e.probe = func(port int, bind string) portState {
		probes++
		cancel() // the person withdraws the stop while the probe is in flight
		return probe(port, bind)
	}

	rows := e.kill(ctx, []ports.ListeningPort{listener(3000, 42)}, []Target{{Port: 3000}},
		Options{Grace: time.Hour})

	// One probe reported the port still open; that answer must not be acted on.
	if probes != 1 {
		t.Fatalf("kept probing after the request was withdrawn: %d probes", probes)
	}
	if len(m.signals) != 1 || m.signals[0].force {
		t.Fatalf("signals = %+v, want the accepted SIGTERM and no escalation", m.signals)
	}
	if m.clock.slept != 0 {
		t.Fatalf("slept %v after the request was withdrawn", m.clock.slept)
	}
	if rows[0].OK {
		t.Fatalf("row = %+v, want the interrupted stop reported as unfinished", rows[0])
	}
}

// TestCancelledProbeThatFindsThePortClosedStillReportsUnfinished pins the check
// that sits between the probe and the pending set. A cancellation that lands
// while the probe is in flight can make the probe report the port closed, and
// "the port is free" is exactly the answer that ends escalation early. Ending
// it there would leave the row claiming a completed stop for a request that was
// withdrawn before anyone knew whether the process had exited.
func TestCancelledProbeThatFindsThePortClosedStillReportsUnfinished(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newMachine(ProcessTable{42: {PID: 42}})
	m.ports[3000] = portOpen
	e := m.engine()

	probe := e.probe
	e.probe = func(port int, bind string) portState {
		cancel()
		// The listener went away by itself just as the request was withdrawn.
		return portClosed
	}
	_ = probe

	rows := e.kill(ctx, []ports.ListeningPort{listener(3000, 42)}, []Target{{Port: 3000}},
		Options{Grace: time.Hour})

	if len(m.signals) != 1 || m.signals[0].force {
		t.Fatalf("signals = %+v, want no escalation", m.signals)
	}
	if rows[0].OK {
		t.Fatalf("row = %+v, want an interrupted stop reported as unfinished", rows[0])
	}
}

// clockThatWithdrawsAfterWaiting lets one wait finish and then withdraws the
// request, which is the instant only the loop's own cancellation check covers.
type clockThatWithdrawsAfterWaiting struct {
	fakeClock
	cancel context.CancelFunc
	waits  int
}

func (c *clockThatWithdrawsAfterWaiting) Wait(ctx context.Context, d time.Duration) bool {
	c.fakeClock.Wait(ctx, d)
	c.waits++
	if c.waits == 1 {
		c.cancel()
	}
	return true
}

// TestWithdrawalAfterACompletedWaitTouchesNoFurtherProcess pins the loop's own
// cancellation check. A wait that finished normally says nothing about the
// request still being wanted, so the next round must re-check before it probes
// the machine again.
func TestWithdrawalAfterACompletedWaitTouchesNoFurtherProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newMachine(ProcessTable{42: {PID: 42}})
	m.ports[3000] = portOpen
	e := m.engine()
	clock := &clockThatWithdrawsAfterWaiting{fakeClock: *m.clock, cancel: cancel}
	e.clock = clock

	probes := 0
	probe := e.probe
	e.probe = func(port int, bind string) portState { probes++; return probe(port, bind) }

	e.kill(ctx, []ports.ListeningPort{listener(3000, 42)}, []Target{{Port: 3000}}, Options{Grace: time.Hour})

	if probes != 1 {
		t.Fatalf("probed %d times; the loop kept going after the request was withdrawn", probes)
	}
}
