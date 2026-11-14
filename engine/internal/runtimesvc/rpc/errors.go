package rpc

import "fmt"

// The failure vocabulary.
//
// Every failure the runtime service can report is declared exactly once, here, with the
// one thing a caller needs in order to act: whether repeating the request can
// help. An agent reads the wire code, looks up its kind, and decides; it never
// has to hold a list of names.
//
// Three rules keep this table honest, and each one is a test:
//
//   - every entry has a producer somewhere in the engine, so the runtime service cannot
//     promise a code it never returns;
//   - every entry is named in docs/cli.md's "stable codes" sentence, so an agent
//     that reads the contract can branch on every failure it may receive;
//   - the two sides are compared by name, so neither can drift from the other.

// Kind is what a caller should do about a failure. It is the only field an
// agent branches on: the wire number is transport and the name is an identity,
// but this is the decision.
type Kind uint8

const (
	// Refusal means the request itself is wrong. Sending it again unchanged
	// produces the same answer, so the caller has to change something: the
	// selector, the path, the manifest, or its assumption about what exists.
	Refusal Kind = iota + 1
	// Condition means the request was well formed and the world was not ready.
	// Waiting or retrying is reasonable; the observation codes are the clearest
	// case, because "I could not see" is not "nothing is there".
	Condition
	// Fault means the engine is broken. The caller cannot act on it beyond
	// reporting it, and it is never the answer to a recoverable situation.
	Fault
)

// String is the word the schema and the tests use.
func (k Kind) String() string {
	switch k {
	case Refusal:
		return "refusal"
	case Condition:
		return "condition"
	case Fault:
		return "fault"
	}
	return "unknown"
}

// failure is one entry of the vocabulary: its wire number, its stable name, and
// what the caller should do about it.
type failure struct {
	number int
	name   string
	kind   Kind
}

// The wire numbers below are the JSON-RPC codes. The name is what a client
// branches on; the number is only how it travels.
const (
	// CodeInvalidParams keeps JSON-RPC's reserved value, so a client that knows
	// only the base spec still classifies a bad request correctly.
	CodeInvalidParams = -32602

	CodeInternal        = 1000 // an engine fault, not a caller mistake
	CodeNotFound        = 1001
	CodeAmbiguous       = 1002 // the selector matched more than one thing
	CodePermission      = 1003
	CodeUnsupported     = 1004 // this platform cannot do it
	CodeBusy            = 1005 // another operation holds what this one needs
	CodeInvalidConfig   = 1006 // the manifest does not parse or validate
	CodeInvalidSelector = 1009
	CodeOutsideHome     = 1010 // a path the runtime service refuses to touch
	CodeConflict        = 1011 // the name is taken

	// Observation failures belong to the collector, not to the engine. They are
	// conditions rather than refusals because a sandboxed caller can retry
	// elsewhere, and they are distinct from each other because an agent decides
	// differently: "empty" is a fact, "blind" and "bind_denied" are not.
	CodeObservationEmpty        = 1013
	CodeObservationIncomplete   = 1014
	CodeObservationUnrecognized = 1015
	CodeObservationBlind        = 1016
	CodeObservationBindDenied   = 1017

	// 1200-1299 belong to sessions and reservations.
	CodeSessionNotFound = 1200
	CodeClaimConflict   = 1201 // another service reserved the port first
)

// failures is the whole vocabulary, in numeric order. Numbers retired from a
// shipped build are not reused, and numbers that never shipped are not kept:
// this table lists what the runtime service can return today.
var failures = []failure{
	{CodeInvalidParams, "invalid_params", Refusal},

	{CodeInternal, "internal", Fault},
	{CodeNotFound, "not_found", Refusal},
	{CodeAmbiguous, "ambiguous", Refusal},
	{CodePermission, "permission_denied", Refusal},
	{CodeUnsupported, "unsupported_platform", Refusal},
	{CodeBusy, "runtime_service_busy", Condition},
	{CodeInvalidConfig, "invalid_config", Refusal},
	{CodeInvalidSelector, "invalid_selector", Refusal},
	{CodeOutsideHome, "outside_home", Refusal},
	{CodeConflict, "conflict", Refusal},

	{CodeObservationEmpty, "observation_empty", Condition},
	{CodeObservationIncomplete, "observation_incomplete", Condition},
	{CodeObservationUnrecognized, "observation_unrecognized", Condition},
	{CodeObservationBlind, "observation_blind", Condition},
	{CodeObservationBindDenied, "observation_bind_denied", Condition},

	{CodeSessionNotFound, "session_not_found", Refusal},
	{CodeClaimConflict, "claim_conflict", Condition},
}

// vocabulary is failures indexed by number.
var vocabulary = func() map[int]failure {
	byNumber := make(map[int]failure, len(failures))
	for _, entry := range failures {
		byNumber[entry.number] = entry
	}
	return byNumber
}()

// CodeName returns the stable name for a wire number. An unknown number answers
// "internal" rather than an empty string, so a client never receives a code it
// has no branch for.
func CodeName(code int) string {
	if entry, known := vocabulary[code]; known {
		return entry.name
	}
	return "internal"
}

// KindOf returns what a caller should do about a failure with this number. An
// unknown number is a fault: the runtime service sent something it cannot account for.
func KindOf(code int) Kind {
	if entry, known := vocabulary[code]; known {
		return entry.kind
	}
	return Fault
}

// FailureNames returns every name in the vocabulary, in numeric order. The
// contract test compares this list with docs/cli.md.
func FailureNames() []string {
	names := make([]string, len(failures))
	for i, entry := range failures {
		names[i] = entry.name
	}
	return names
}

// ErrorData is the payload every error carries. Clients branch on Code — the
// stable name — and never on the numeric JSON-RPC code, which is only how it
// travels.
type ErrorData struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Hint   string `json:"hint"`
}

// Error is a JSON-RPC error object. It is both the wire shape and the Go error
// a handler returns, so a handler builds one and hands it straight back.
type Error struct {
	Code    int       `json:"code"`
	Message string    `json:"message"`
	Data    ErrorData `json:"data"`
}

// Error satisfies the error interface with the human-readable detail.
func (failure *Error) Error() string { return failure.Message }

// Kind is what a caller should do about this failure.
func (failure *Error) Kind() Kind { return KindOf(failure.Code) }

// Errorf builds an error from a format string. Detail and message are always
// the same text, so there is one place they could drift apart and it is here.
func Errorf(code int, format string, args ...any) *Error {
	detail := fmt.Sprintf(format, args...)
	return &Error{
		Code:    code,
		Message: detail,
		Data:    ErrorData{Code: CodeName(code), Detail: detail},
	}
}

// NewError is Errorf with a fixed detail, plus the hint a CLI prints to suggest
// what to do next.
func NewError(code int, detail, hint string) *Error {
	failure := Errorf(code, "%s", detail)
	failure.Data.Hint = hint
	return failure
}
