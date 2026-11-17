package cmd

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/usersvc"
	"github.com/spf13/cobra"
)

// fakeSupervisor stands in for launchd or systemd. It records what the command asked
// for, and lets a test put the socket a real unit would have created.
type fakeSupervisor struct {
	installed  bool
	active     bool
	activeErr  error
	startErr   error
	restartErr error
	onStart    func()
	onRestart  func()
	calls      []string
}

func (f *fakeSupervisor) Name() string     { return "fake-supervisor" }
func (f *fakeSupervisor) UnitPath() string { return "/fake/option-berth.unit" }
func (f *fakeSupervisor) Installed() bool  { return f.installed }

func (f *fakeSupervisor) Active(context.Context) (bool, error) {
	f.calls = append(f.calls, "active")
	return f.active, f.activeErr
}

func (f *fakeSupervisor) Start(context.Context) error {
	f.calls = append(f.calls, "start")
	if f.startErr != nil {
		return f.startErr
	}
	if f.onStart != nil {
		f.onStart()
	}
	return nil
}

func (f *fakeSupervisor) Restart(context.Context) error {
	f.calls = append(f.calls, "restart")
	if f.restartErr != nil {
		return f.restartErr
	}
	if f.onRestart != nil {
		f.onRestart()
	}
	return nil
}

// useFakeSupervisor makes supervisor the one the command talks to for this test.
func useFakeSupervisor(t *testing.T, supervisor usersvc.Supervisor) {
	t.Helper()
	previous := runtimeServiceSupervisor
	runtimeServiceSupervisor = supervisor
	// A test must not spend the real budget waiting for a socket that never comes.
	previousTimeout := supervisorSocketTimeout
	supervisorSocketTimeout = 5 * time.Second
	t.Cleanup(func() {
		runtimeServiceSupervisor = previous
		supervisorSocketTimeout = previousTimeout
	})
}

// watchDetachedChildren reports whether the command forked a `runtime service run`
// child of its own. Any child is a second owner next to a unit, so every unit test
// asserts this stayed false.
func watchDetachedChildren(t *testing.T) *bool {
	t.Helper()
	previous := startRuntimeServiceChild
	spawned := false
	startRuntimeServiceChild = func(*exec.Cmd) (int, error) {
		spawned = true
		return 0, errors.New("test: the CLI must not start a runtime service of its own here")
	}
	t.Cleanup(func() { startRuntimeServiceChild = previous })
	return &spawned
}

// listenAt makes socket accept connections without speaking the protocol. It is what
// a supervisor-started runtime service looks like to a caller that only waits.
func listenAt(t *testing.T, socket string) {
	t.Helper()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listening on %s: %v", socket, err)
	}
	t.Cleanup(func() { _ = listener.Close() })
}

// The unit owns the running runtime service, so `restart` replaces its process. It must
// not stop and re-detach: that would leave a second owner beside the unit.
func TestRestartReplacesTheActiveUnit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket harness does not apply to named pipes")
	}
	socket := isolateRuntimeServicePaths(t)
	setRuntimeServiceJSONFlag(t, false)
	supervisor := &fakeSupervisor{installed: true, active: true}
	supervisor.onRestart = func() { listenAt(t, socket) }
	useFakeSupervisor(t, supervisor)
	spawned := watchDetachedChildren(t)

	if err := runtimeServiceRestartRun(&cobra.Command{}, nil); err != nil {
		t.Fatalf("restart = %v, want nil", err)
	}
	if got := strings.Join(supervisor.calls, ","); got != "active,restart" {
		t.Fatalf("supervisor calls = %q, want active then restart", got)
	}
	if *spawned {
		t.Error("restart forked a detached runtime service beside the unit")
	}
}

// An installed unit that is not running is started, not restarted: `restart` on a
// job the supervisor has not loaded fails, and the machine would be left with no
// runtime service at all.
func TestRestartStartsAnInactiveUnit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket harness does not apply to named pipes")
	}
	socket := isolateRuntimeServicePaths(t)
	setRuntimeServiceJSONFlag(t, false)
	supervisor := &fakeSupervisor{installed: true, active: false}
	supervisor.onStart = func() { listenAt(t, socket) }
	useFakeSupervisor(t, supervisor)
	spawned := watchDetachedChildren(t)

	if err := runtimeServiceRestartRun(&cobra.Command{}, nil); err != nil {
		t.Fatalf("restart = %v, want nil", err)
	}
	if got := strings.Join(supervisor.calls, ","); got != "active,start" {
		t.Fatalf("supervisor calls = %q, want active then start", got)
	}
	if *spawned {
		t.Error("restart forked a detached runtime service beside the unit")
	}
}

// A supervisor that refuses is the answer. Falling back to a detached child here
// would hide a broken unit behind a runtime service the supervisor cannot see.
func TestRestartReportsTheSupervisorFailure(t *testing.T) {
	socket := isolateRuntimeServicePaths(t)
	setRuntimeServiceJSONFlag(t, false)
	supervisor := &fakeSupervisor{
		installed:  true,
		active:     true,
		restartErr: errors.New("launchctl kickstart -k gui/501/com.sheathedsharp.option-berth.runtime: exit status 113"),
	}
	useFakeSupervisor(t, supervisor)
	spawned := watchDetachedChildren(t)

	err := runtimeServiceRestartRun(&cobra.Command{}, nil)
	if err == nil {
		t.Fatalf("restart = nil with a supervisor that failed; socket %s", socket)
	}
	if !strings.Contains(err.Error(), "exit status 113") {
		t.Errorf("error = %v, want the supervisor's own failure", err)
	}
	if *spawned {
		t.Error("restart fell back to a detached runtime service after the unit failed")
	}
}

// Without a unit the old behaviour stands: stop what is running and detach a new
// one. Nothing may be asked of a supervisor that installed nothing.
func TestRestartKeepsTheDetachedPathWithoutAUnit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket harness does not apply to named pipes")
	}
	socket := isolateRuntimeServicePaths(t)
	setRuntimeServiceJSONFlag(t, false)
	supervisor := &fakeSupervisor{installed: false}
	useFakeSupervisor(t, supervisor)
	spawned := watchDetachedChildren(t)
	// The child this test does not really start is the one that would have made the
	// socket appear; make it appear instead, so the command can finish.
	previous := startRuntimeServiceChild
	startRuntimeServiceChild = func(*exec.Cmd) (int, error) {
		*spawned = true
		listenAt(t, socket)
		return 4242, nil
	}
	t.Cleanup(func() { startRuntimeServiceChild = previous })

	if err := runtimeServiceRestartRun(&cobra.Command{}, nil); err != nil {
		t.Fatalf("restart = %v, want nil", err)
	}
	if len(supervisor.calls) != 0 {
		t.Fatalf("supervisor calls = %q, want none without an installed unit", supervisor.calls)
	}
	if !*spawned {
		t.Error("restart did not detach a runtime service on a machine without a unit")
	}
}

// A write command that finds nothing running hands the start to the unit. The unit
// is the machine's runtime service owner; a detached child beside it would be a
// second one.
func TestConnectForWriteStartsTheUnitInsteadOfAChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket harness does not apply to named pipes")
	}
	socket := isolateRuntimeServicePaths(t)
	t.Setenv(client.NoAutostartEnv, "")
	supervisor := &fakeSupervisor{installed: true}
	supervisor.onStart = func() { runRuntimeServiceAt(t, socket) }
	useFakeSupervisor(t, supervisor)
	spawned := watchDetachedChildren(t)

	conn, err := connectForWrite(context.Background())
	if err != nil {
		t.Fatalf("connectForWrite = %v, want a connection to the unit's runtime service", err)
	}
	defer conn.Close()
	if got := strings.Join(supervisor.calls, ","); got != "start" {
		t.Fatalf("supervisor calls = %q, want one start", got)
	}
	if *spawned {
		t.Error("connectForWrite forked a detached runtime service beside the unit")
	}
}

// A unit that will not start leaves the caller where it was. The refusal must then
// point at the supervisor and carry its failure: the old "run --detach" hint would
// start a second owner beside the unit the machine already has.
func TestConnectForWriteReportsAFailedUnitStart(t *testing.T) {
	isolateRuntimeServicePaths(t)
	t.Setenv(client.NoAutostartEnv, "")
	// The probe is healthy on purpose: an installed unit owns the attempt even when it
	// fails, so the observable fallback must never be reached.
	previousProbe := scanProbe
	scanProbe = func(context.Context) ports.ProbeResult {
		return ports.ProbeResult{State: ports.ObservationObservable}
	}
	t.Cleanup(func() { scanProbe = previousProbe })
	supervisor := &fakeSupervisor{
		installed: true,
		startErr:  errors.New("systemctl --user start option-berth-runtime.service: exit status 1"),
	}
	useFakeSupervisor(t, supervisor)
	spawned := watchDetachedChildren(t)

	_, err := connectForWrite(context.Background())
	if err == nil {
		t.Fatal("connectForWrite = nil with no reachable runtime service")
	}
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("error = %v, want the supervisor's own failure", err)
	}
	if strings.Contains(err.Error(), "run --detach") {
		t.Errorf("error = %v, want the supervisor rather than a second owner", err)
	}
	if got := strings.Join(supervisor.calls, ","); got != "start" {
		t.Fatalf("supervisor calls = %q, want one start", got)
	}
	if *spawned {
		t.Error("connectForWrite forked a detached runtime service after the unit failed to start")
	}
}

// `run --detach` is the one remaining way to put a second owner beside an installed
// unit. The command refuses instead of leaving the supervisor a service it cannot bind.
func TestRunDetachRefusesWhenAUnitIsInstalled(t *testing.T) {
	isolateRuntimeServicePaths(t)
	t.Setenv(runtimesvc.AllowTestRuntimeServiceEnv, "1")
	supervisor := &fakeSupervisor{installed: true}
	useFakeSupervisor(t, supervisor)
	spawned := watchDetachedChildren(t)
	previous := runtimeServiceDetach
	runtimeServiceDetach = true
	t.Cleanup(func() { runtimeServiceDetach = previous })

	err := runtimeServiceRun(runtimeServiceRunCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "user-level service unit is installed") {
		t.Fatalf("error = %v, want the unit refusal", err)
	}
	if *spawned {
		t.Error("run --detach forked a runtime service beside the installed unit")
	}
	if len(supervisor.calls) != 0 {
		t.Fatalf("supervisor calls = %q, want none from run --detach", supervisor.calls)
	}
}

// Autostart is off for the whole process tree in tests and CI. That switch outranks
// the unit as well: asking a supervisor to start a service is autostart.
func TestConnectForWriteLeavesTheUnitAloneWhenAutostartIsOff(t *testing.T) {
	isolateRuntimeServicePaths(t)
	t.Setenv(client.NoAutostartEnv, "1")
	blindObservation(t)
	supervisor := &fakeSupervisor{installed: true}
	useFakeSupervisor(t, supervisor)

	if _, err := connectForWrite(context.Background()); err == nil {
		t.Fatal("connectForWrite = nil with no runtime service and autostart disabled")
	}
	if len(supervisor.calls) != 0 {
		t.Fatalf("supervisor calls = %q, want none while %s is set", supervisor.calls, client.NoAutostartEnv)
	}
}
