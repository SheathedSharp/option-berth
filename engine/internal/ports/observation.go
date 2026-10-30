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
	// no listening table. A successful exit is not evidence that nothing listens.
	CodeObservationEmpty = "observation_empty"
	// CodeObservationIncomplete means the pass could not be trusted: a row was
	// malformed, or the collector put diagnostics on stderr. The pass is
	// discarded rather than published as a possibly-partial table.
	CodeObservationIncomplete = "observation_incomplete"
)

// Observation hints name the next step, not just the symptom. They are shown
// verbatim by the CLI, the daemon and `oberth doctor`.
const (
	observationEmptyHint = "the collector exited successfully but returned no listening table; " +
		"an agent sandbox may be blocking system introspection — retry outside the sandbox, " +
		"or run `oberth doctor --only listener_observation` to check this process"
	observationIncompleteHint = "the collector's output could not be trusted, so no listener is reported absent; " +
		"run `oberth doctor --only listener_observation`, then retry outside an agent sandbox if it also fails"
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
