package killer

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestPIDClaimSurvivesListenerResolution(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	w := newWorld(ProcessTable{777: {PID: 777}})
	w.startTimes[777] = base.Add(time.Hour)
	rows := w.engine().kill(context.Background(), []ports.ListeningPort{listener(3000, 777)},
		[]Target{{PID: 777, StartedAt: base}}, Options{Force: true})
	if len(w.signals) != 0 || len(rows) != 1 || rows[0].OK || rows[0].Code != CodeNotFound || rows[0].Method != state.MethodNone {
		t.Fatalf("stale run claim matched listener: signals=%v rows=%+v", w.signals, rows)
	}
}

func TestPIDRevalidatedBetweenPlanningAndSignal(t *testing.T) {
	w := newWorld(ProcessTable{42: {PID: 42}})
	e := w.engine()
	reads := 0
	e.startTime = func(int) (time.Time, bool) {
		reads++
		return time.Unix(int64(reads)*3600, 0), true
	}
	rows := e.kill(context.Background(), nil, []Target{{PID: 42}}, Options{Force: true})
	if len(w.signals) != 0 || len(rows) != 1 || rows[0].OK || rows[0].Method != state.MethodNone {
		t.Fatalf("plan-time evidence was not checked again: signals=%v rows=%+v", w.signals, rows)
	}
}

func TestEscalationRejectsRecycledPIDAndGroup(t *testing.T) {
	for _, group := range []bool{false, true} {
		w := newWorld(ProcessTable{42: {PID: 42}})
		w.startTimes[42] = time.Unix(1000, 0)
		w.open[3000] = true
		if group {
			w.groups[42] = 42
		}
		w.killEffect = func(w *fakeWorld, c signalCall) { w.startTimes[c.pid] = time.Unix(2000, 0) }
		rows := w.engine().kill(context.Background(), []ports.ListeningPort{listener(3000, 42)},
			[]Target{{Port: 3000}}, Options{Grace: time.Second})
		if len(w.signals) != 1 || w.signals[0].force || len(rows) != 1 || rows[0].OK || rows[0].Code != CodeNotFound || rows[0].Method != state.MethodSIGTERM {
			t.Fatalf("group=%v: replacement received escalation: signals=%v rows=%+v", group, w.signals, rows)
		}
	}
}

func TestEscalationRefusesLostKnownBirth(t *testing.T) {
	w := newWorld(ProcessTable{42: {PID: 42}})
	w.startTimes[42] = time.Unix(1000, 0)
	w.open[3000] = true
	w.killEffect = func(w *fakeWorld, c signalCall) { delete(w.startTimes, c.pid) }
	rows := w.engine().kill(context.Background(), []ports.ListeningPort{listener(3000, 42)},
		[]Target{{Port: 3000}}, Options{Grace: time.Second})
	if len(w.signals) != 1 || rows[0].OK || rows[0].Code != CodePermissionDenied || rows[0].Method != state.MethodSIGTERM {
		t.Fatalf("missing evidence became identity approval: signals=%v rows=%+v", w.signals, rows)
	}
}

func TestTreeIdentityChecksDoNotBlockAfterChildExit(t *testing.T) {
	w := newWorld(ProcessTable{10: {PID: 10}, 20: {PID: 20, PPID: 10}, 21: {PID: 21, PPID: 10}})
	for pid := range w.table {
		w.startTimes[pid] = time.Unix(int64(pid)*10, 0)
	}
	w.killEffect = func(w *fakeWorld, c signalCall) {
		delete(w.alive, c.pid)
		delete(w.startTimes, c.pid)
	}
	snapshot := []ports.ListeningPort{listener(3000, 20, func(p *ports.ListeningPort) { p.RunRootPID = 10 })}
	rows := w.engine().kill(context.Background(), snapshot, []Target{{Port: 3000}}, Options{Force: true})
	if !reflect.DeepEqual(w.signalledPIDs(), []int{20, 21, 10}) {
		t.Fatalf("stopped after listener exit: %v", w.signals)
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
	w := newWorld(ProcessTable{42: {PID: 42}})
	w.killEffect = func(*fakeWorld, signalCall) { cancel() }
	snapshot := []ports.ListeningPort{
		listener(3000, 42),
		listener(4000, 90, func(p *ports.ListeningPort) { p.Type = ports.PortTypeDocker; p.DockerContainer = "demo" }),
	}
	rows := w.engine().kill(ctx, snapshot, []Target{{Port: 3000}, {Port: 4000}}, Options{Force: true})
	if len(w.signals) != 1 || len(w.stopped) != 0 || len(rows) != 2 || rows[1].OK || rows[1].Method != state.MethodNone {
		t.Fatalf("cancelled operation continued: signals=%v docker=%v rows=%+v", w.signals, w.stopped, rows)
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
	w := newWorld(ProcessTable{42: {PID: 42}})
	w.open[3000] = true
	e := w.engine()
	e.clock = &cancelSleepClock{fakeClock: *w.clock, cancel: cancel}
	rows := e.kill(ctx, []ports.ListeningPort{listener(3000, 42)}, []Target{{Port: 3000}}, Options{Grace: pollInterval})
	if len(w.signals) != 1 || w.signals[0].force || rows[0].OK || rows[0].Method != state.MethodSIGTERM {
		t.Fatalf("cancelled grace performed escalation or claimed completion: %v %+v", w.signals, rows)
	}
}

func TestInheritedNativeTreeRemainsATreeDuringEscalation(t *testing.T) {
	w := newWorld(ProcessTable{42: {PID: 42}, 43: {PID: 43, PPID: 42}})
	w.open[3000] = true
	e := w.engine()
	e.nativeTree = true
	var trees []bool
	e.signalTree = func(_ int, force bool) error { trees = append(trees, force); return nil }
	snapshot := []ports.ListeningPort{listener(3000, 43, func(p *ports.ListeningPort) { p.RunRootPID = 42 })}
	e.kill(context.Background(), snapshot, []Target{{Port: 3000}}, Options{Grace: time.Second})
	if len(w.signals) != 0 || !reflect.DeepEqual(trees, []bool{false, true}) {
		t.Fatalf("inherited tree was reduced to a process: process=%v tree=%v", w.signals, trees)
	}
}

func TestFailedInitialSignalIsNotEscalated(t *testing.T) {
	w := newWorld(ProcessTable{42: {PID: 42}})
	w.open[3000] = true
	w.failKil[42] = permissionErr(42, errors.New("denied"))
	rows := w.engine().kill(context.Background(), []ports.ListeningPort{listener(3000, 42)}, []Target{{Port: 3000}}, Options{})
	if len(w.signals) != 1 || rows[0].OK || w.clock.slept != 0 {
		t.Fatalf("failed action was retried: %v %+v", w.signals, rows)
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
	w := newWorld(ProcessTable{10: {PID: 10}, 20: {PID: 20, PPID: 10}})
	e := w.engine()
	reads := map[int]int{}
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
	for _, c := range w.signals {
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
	w := newWorld(ProcessTable{})
	e := w.engine()
	e.alive = func(int) bool { t.Fatal("invalid PID reached a system adapter"); return true }
	rows := e.kill(context.Background(), nil, []Target{{PID: int(huge)}}, Options{Force: true})
	if len(rows) != 1 || rows[0].OK || rows[0].Code != CodeInvalidSelector {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestFirstKnownSignalBirthPersistsIntoEscalation(t *testing.T) {
	w := newWorld(ProcessTable{42: {PID: 42}})
	w.open[3000] = true
	e := w.engine()
	reads := 0
	current := time.Unix(1000, 0)
	e.startTime = func(int) (time.Time, bool) {
		reads++
		if reads == 1 {
			return time.Time{}, false
		}
		return current, true
	}
	w.killEffect = func(*fakeWorld, signalCall) { current = current.Add(time.Hour) }
	rows := e.kill(context.Background(), []ports.ListeningPort{listener(3000, 42)}, []Target{{Port: 3000}}, Options{Grace: time.Second})
	if len(w.signals) != 1 || rows[0].OK || rows[0].Code != CodeNotFound {
		t.Fatalf("signals=%v rows=%+v", w.signals, rows)
	}
}
