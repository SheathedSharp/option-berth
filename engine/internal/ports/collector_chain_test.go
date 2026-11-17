package ports

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// linuxSSTable is a `ss -tlnp` capture: the platform's primary Linux table.
const linuxSSTable = `State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process
LISTEN 0      128    127.0.0.1:18120    0.0.0.0:*    users:(("node",pid=4242,fd=20))
`

// collectorAnswer is one command's answer. A tool with no answer exits 0 with
// no output, which is the sandbox shape every collector has to survive.
type collectorAnswer struct {
	out, stderr string
	err         error
}

// fakeCollectors answers each command from a table, so one test can make the
// primary fail and the next rung answer. It records what ran, in order.
type fakeCollectors struct {
	answers map[string]collectorAnswer
	tried   []string
}

func (f *fakeCollectors) run(name string, _ ...string) ([]byte, []byte, error) {
	f.tried = append(f.tried, name)
	answer := f.answers[name]
	return []byte(answer.out), []byte(answer.stderr), answer.err
}

// The reason Linux has a second rung: a minimal image or a host without
// iproute2 has no ss, and the kernel table is still there to read.
func TestLinuxChainFallsBackWhenSSIsNotInstalled(t *testing.T) {
	run := &fakeCollectors{answers: map[string]collectorAnswer{
		"ss":      {err: exec.ErrNotFound},
		"netstat": {out: linuxNetstatTable},
	}}
	rows, source, err := collectListenersSource("linux", run.run)
	if err != nil {
		t.Fatalf("netstat answered but the scan failed: %v", err)
	}
	if source != "netstat" {
		t.Fatalf("source = %q, want the collector that answered", source)
	}
	if len(rows) != 5 || rows[2].Port != 18122 || rows[2].PID != 4242 {
		t.Fatalf("rows = %+v, want netstat's table", rows)
	}
	if want := []string{"ss", "netstat"}; !reflect.DeepEqual(run.tried, want) {
		t.Fatalf("collectors tried = %v, want %v", run.tried, want)
	}
}

// Two failed collectors are one fact about this process, reported once: the
// primary's code says what was refused, and the alternative's reason has to
// survive in the same sentence.
func TestChainKeepsThePrimaryCodeAndEveryReason(t *testing.T) {
	run := &fakeCollectors{answers: map[string]collectorAnswer{
		"netstat": {stderr: "netstat: permission denied\n", err: collectorExit(1)},
	}}
	rows, source, err := collectListenersSource("linux", run.run)
	if len(rows) != 0 || source != "" {
		t.Fatalf("rows=%+v source=%q, want nothing published", rows, source)
	}
	var observed *ObservationError
	if !errors.As(err, &observed) || observed.Code != CodeObservationEmpty {
		t.Fatalf("error = %+v, want the primary's %q", err, CodeObservationEmpty)
	}
	for _, want := range []string{
		"ss: exited 0 but printed no listening table",
		"netstat: incomplete listener observation",
		"permission denied",
	} {
		if !strings.Contains(observed.Error(), want) {
			t.Fatalf("detail = %q, want it to contain %q", observed.Error(), want)
		}
	}
	if want := []string{"ss", "netstat"}; !reflect.DeepEqual(run.tried, want) {
		t.Fatalf("collectors tried = %v, want %v", run.tried, want)
	}
}

// The primary's own table is the platform's answer: it is never published as a
// source, and no other collector runs behind it.
func TestChainDoesNotNameThePrimaryCollector(t *testing.T) {
	run := &fakeCollectors{answers: map[string]collectorAnswer{"ss": {out: linuxSSTable}}}
	rows, source, err := collectListenersSource("linux", run.run)
	if err != nil || source != "" || len(rows) != 1 {
		t.Fatalf("rows=%+v source=%q err=%v, want the unnamed primary table", rows, source, err)
	}
	if want := []string{"ss"}; !reflect.DeepEqual(run.tried, want) {
		t.Fatalf("collectors tried = %v, want %v", run.tried, want)
	}
}

// Readable rows are facts and end the round, failure or not. Asking another tool
// would replace this machine's own answer with a different table, and the ports
// that were read would look like they had never been seen.
func TestChainStopsAfterReadableRows(t *testing.T) {
	run := &fakeCollectors{answers: map[string]collectorAnswer{
		"netstat": {out: darwinTCPHeader +
			"tcp4 0 0 127.0.0.1.18120 *.* LISTEN 0 0 128 128 node:42 0 0\n" +
			"tcp4 0 0 127.0.0.1.18121 *.* LISTEN\n"},
	}}
	rows, source, err := collectListenersSource("darwin", run.run)
	if len(rows) != 1 || rows[0].Port != 18120 || source != "" {
		t.Fatalf("rows=%+v source=%q, want the readable row from the primary", rows, source)
	}
	var observed *ObservationError
	if !errors.As(err, &observed) || observed.Code != CodeObservationIncomplete || !observed.Partial {
		t.Fatalf("error = %+v, want the primary's partial verdict", err)
	}
	if want := []string{"netstat"}; !reflect.DeepEqual(run.tried, want) {
		t.Fatalf("collectors tried = %v, want only the primary", run.tried)
	}
}

// A decoder that rejects the table it was given is reporting a problem no other
// tool can answer: switching commands would hide it behind a different format.
func TestChainDoesNotHideADecoderProblem(t *testing.T) {
	run := &fakeCollectors{answers: map[string]collectorAnswer{
		"netstat": {out: darwinTCPHeader + "tcp4 0\n"},
	}}
	if _, source, err := collectListenersSource("darwin", run.run); err == nil || source != "" {
		t.Fatalf("source=%q err=%v, want the decoder's own failure", source, err)
	}
	if want := []string{"netstat"}; !reflect.DeepEqual(run.tried, want) {
		t.Fatalf("collectors tried = %v, want only the primary", run.tried)
	}
}

// A withdrawn observation belongs to whoever withdrew it. Starting another
// command would answer a caller that is already gone.
func TestChainRunsNothingAfterCancellation(t *testing.T) {
	run := &fakeCollectors{answers: map[string]collectorAnswer{"ss": {err: context.Canceled}}}
	rows, source, err := collectListenersSource("linux", run.run)
	if len(rows) != 0 || source != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("rows=%+v source=%q err=%v, want the cancellation itself", rows, source, err)
	}
	if want := []string{"ss"}; !reflect.DeepEqual(run.tried, want) {
		t.Fatalf("collectors tried = %v, want only the primary", run.tried)
	}
}

// An alternative that ran clean and printed no row is not an idle machine. Two
// silent collectors stay the primary's suspicious silence, and the self-probe
// is still the one that decides.
func TestChainDoesNotPublishASilentAlternative(t *testing.T) {
	run := &fakeCollectors{answers: map[string]collectorAnswer{
		"ss":      {err: exec.ErrNotFound},
		"netstat": {out: "Proto Recv-Q Send-Q Local Address Foreign Address State PID/Program name\n"},
	}}
	rows, source, err := collectListenersSource("linux", run.run)
	if len(rows) != 0 || source != "" {
		t.Fatalf("rows=%+v source=%q, want nothing published", rows, source)
	}
	if err == nil || !strings.Contains(err.Error(), "netstat: printed no listening table") {
		t.Fatalf("error = %v, want the silent alternative named", err)
	}
}

// Every alternative declares at least one limit, and every chain has a primary.
// Only the platform's own table is read as authoritative, so a second collector
// can never turn a missing port into a negative fact.
func TestEveryChainIsNamedAndEveryAlternativeIsLimited(t *testing.T) {
	for _, platform := range []string{"darwin", "linux", "windows"} {
		rungs := chain(platform)
		if len(rungs) == 0 {
			t.Fatalf("platform %s has no collector", platform)
		}
		names := make(map[string]bool, len(rungs))
		for i, rung := range rungs {
			if rung.Name == "" || rung.collect == nil {
				t.Fatalf("platform %s rung %d is unnamed or cannot collect", platform, i)
			}
			if names[rung.Name] {
				t.Fatalf("platform %s repeats the collector %q", platform, rung.Name)
			}
			names[rung.Name] = true
			if i > 0 && len(rung.Capabilities.limits()) == 0 {
				t.Fatalf("platform %s alternative %q declares no limit", platform, rung.Name)
			}
		}
	}
	if rungs := chain("unsupported"); len(rungs) != 0 {
		t.Fatalf("unsupported platform has collectors: %+v", rungs)
	}
}

// The published sentence says what the collector that answered cannot promise,
// so a reader can tell "no one holds this port" from "this table cannot show
// that owner".
func TestNarrowSourceDetailIsDerivedFromCapabilities(t *testing.T) {
	if got := narrowSourceDetail("darwin", ""); got != "" {
		t.Fatalf("primary detail = %q, want nothing", got)
	}
	lsof := narrowSourceDetail("darwin", "lsof")
	for _, want := range []string{
		"read with lsof",
		"cannot see sockets owned by other users",
		"reports a dual-stack wildcard socket under one address family only",
		"a port missing from it is unknown, not free",
	} {
		if !strings.Contains(lsof, want) {
			t.Fatalf("detail = %q, want it to contain %q", lsof, want)
		}
	}
	if strings.Contains(lsof, "cannot name the process") {
		t.Fatalf("detail = %q, want no missing-attribution clause for lsof", lsof)
	}
	netstat := narrowSourceDetail("linux", "netstat")
	if !strings.Contains(netstat, "read with netstat") ||
		!strings.Contains(netstat, "one address family only") ||
		!strings.Contains(netstat, "unknown, not free") {
		t.Fatalf("detail = %q, want netstat's own limits", netstat)
	}
	if strings.Contains(netstat, "other users") {
		t.Fatalf("detail = %q, want no coverage claim netstat does not have", netstat)
	}
	unknown := narrowSourceDetail("linux", "runtime_registry")
	if !strings.Contains(unknown, "runtime_registry") || !strings.Contains(unknown, "unknown, not free") {
		t.Fatalf("detail = %q, want an unknown source to stay conservative", unknown)
	}
}
