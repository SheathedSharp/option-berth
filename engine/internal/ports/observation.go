package ports

import "fmt"

// A sandboxed collector does not fail like a missing executable. macOS
// seatbelt lets `netstat` run, denies the sysctl behind the TCP table, and
// netstat still exits 0 with an empty stdout and one diagnostic line on
// stderr; an output filter can also leave a row the decoder cannot read, which
// invalidates the whole pass. Both used to reach the CLI as "internal", so an
// agent could not tell "nothing is listening" from "this process is not
// allowed to look". These codes are part of the CLI/RPC contract (docs/cli.md).
const (
	// CodeObservationEmpty means the collector exited successfully but printed
	// no listening table at all. This is suspicious, not proof of an idle
	// machine: the self-probe decides whether the process is blind.
	CodeObservationEmpty = "observation_empty"
	// CodeObservationUnrecognized means the collector printed something, but
	// no table header the decoder knows. That is a format problem, not an
	// empty machine; the hint asks for the header rather than a sandbox change.
	CodeObservationUnrecognized = "observation_unrecognized"
	// CodeObservationIncomplete means the pass could not be trusted: a row was
	// malformed, or the collector put diagnostics on stderr. The pass is
	// discarded rather than published as a possibly-partial table.
	CodeObservationIncomplete = "observation_incomplete"
	// CodeObservationBlind is the self-probe's verdict when this process can
	// bind a port but cannot see its own listener in the table.
	CodeObservationBlind = "observation_blind"
	// CodeObservationBindDenied is the self-probe's verdict when this process
	// cannot bind a local port at all.
	CodeObservationBindDenied = "observation_bind_denied"
)

// Observation hints name the next step, not just the symptom. They are shown
// verbatim by the CLI, the daemon and `oberth doctor`.
const (
	observationEmptyHint = "the collector exited successfully but returned no listening table; " +
		"run `oberth doctor --only listener_observation` to determine whether this process can see its own listener"
	observationUnrecognizedHint = "the collector printed no table header option-berth recognizes; " +
		"upgrade option-berth if the table changed, or report the header line"
	observationIncompleteHint = "the collector's output could not be trusted, so no listener is reported absent; " +
		"run `oberth doctor --only listener_observation`, then retry outside an agent sandbox if it also fails"
	observationBlindHint = "this process cannot see its own listener, so an empty listener list is not evidence that nothing runs; " +
		"retry outside the sandbox or run `oberth doctor --only listener_observation`"
	observationBindDeniedHint = "this process cannot bind a local port, so services started from here cannot listen either; " +
		"retry outside the sandbox or allow option-berth's network access"
)

// ObservationError is a listener observation that failed for a reason the
// caller can name and act on. Callers branch on Code and show Hint; they must
// not parse Error().
type ObservationError struct {
	Code   string
	Detail string
	Hint   string
	Err    error
}

func (e *ObservationError) Error() string { return e.Detail }

// Unwrap keeps the underlying command error available to errors.Is, so a
// cancelled context stays distinguishable from a collector that answered.
func (e *ObservationError) Unwrap() error { return e.Err }

func observationf(code, hint, format string, args ...any) *ObservationError {
	return &ObservationError{Code: code, Detail: fmt.Sprintf(format, args...), Hint: hint}
}

// observationErr keeps the command's own error reachable through errors.Is
// while still giving the pass a stable code and hint.
func observationErr(err error, code, hint, format string, args ...any) *ObservationError {
	return &ObservationError{
		Code: code, Detail: fmt.Sprintf(format, args...), Hint: hint, Err: err,
	}
}
