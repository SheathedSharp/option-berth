package rpc

import "fmt"

// ErrorData is the payload every error carries. Clients branch on Code — the
// stable string — and never on the numeric JSON-RPC code, which is only the wire
// transport for it.
type ErrorData struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Hint   string `json:"hint"`
}

// Error is a JSON-RPC error object. It is both the wire shape and the Go error
// the daemon returns, so a handler can build one and hand it straight back.
type Error struct {
	Code    int       `json:"code"`
	Message string    `json:"message"`
	Data    ErrorData `json:"data"`
}

// Error satisfies the error interface with the human-readable detail.
func (failure *Error) Error() string { return failure.Message }

// The numeric codes below are the JSON-RPC wire codes. Their stable string
// names live in errorRegistry, one line each, so a code cannot be added without
// deciding what clients will see.
const (
	// CodeInvalidParams keeps JSON-RPC's reserved value, so a client that only
	// knows the base spec still classifies a bad request correctly.
	CodeInvalidParams = -32602

	// The 1000-range codes are this protocol's own.
	CodeInternal        = 1000 // an engine bug, not a caller mistake
	CodeNotFound        = 1001
	CodeAmbiguous       = 1002 // the selector matched more than one thing
	CodePermission      = 1003
	CodeUnsupported     = 1004 // the platform cannot do this
	CodeBusy            = 1005 // the daemon is doing something that excludes this
	CodeInvalidConfig   = 1006 // the manifest does not parse or validate
	CodeAlreadyRunning  = 1007
	CodeTimeout         = 1008 // the wait expired; the operation may still finish
	CodeInvalidSelector = 1009
	CodeOutsideHome     = 1010 // a path the daemon refuses to touch
	CodeConflict        = 1011

	// CodeNotConfigured is kept for clients that know the shared registry; no
	// core command emits it today.
	CodeNotConfigured = 1012

	// Observation failures belong to the collector, not to engine bugs. A
	// sandboxed agent reads the code to decide whether it may retry elsewhere
	// or has to distrust an "absent listener" answer — it must not, which is
	// why these are distinct from CodeNotFound and from each other.
	CodeObservationEmpty        = 1013
	CodeObservationIncomplete   = 1014
	CodeObservationUnrecognized = 1015
	CodeObservationBlind        = 1016
	CodeObservationBindDenied   = 1017

	// 1100-1199 belong to shares and proxies. 1101-1106 and 1109 were the
	// provider design and were retired before anything returned them; they stay
	// unused rather than reassigned, because a client may have cached them.
	CodeTargetNotListening = 1100 // the share's target has no listener
	CodeShareLimitReached  = 1107 // one share at a time; this is the second
	CodeListenPortInUse    = 1108 // the port a share would use is taken
	CodeNotSignedIn        = 1110
	CodeShareExpired       = 1111
	CodeRelayUnreachable   = 1112
	CodeShareBlocked       = 1113
	CodeTargetNotHTTP      = 1114 // only HTTP targets can be shared

	// 1200-1299 belong to sessions and claims.
	CodeSessionNotFound = 1200
	CodeClaimConflict   = 1201
)

// errorRegistry pairs each code with the string clients branch on. The
// declarations above and the entries below are the whole registry; CodeName is
// the only reader.
var errorRegistry = []struct {
	code int
	name string
}{
	{CodeInvalidParams, "invalid_params"},
	{CodeInternal, "internal"},
	{CodeNotFound, "not_found"},
	{CodeAmbiguous, "ambiguous"},
	{CodePermission, "permission_denied"},
	{CodeUnsupported, "unsupported_platform"},
	{CodeBusy, "daemon_busy"},
	{CodeInvalidConfig, "invalid_config"},
	{CodeAlreadyRunning, "already_running"},
	{CodeTimeout, "timeout"},
	{CodeInvalidSelector, "invalid_selector"},
	{CodeOutsideHome, "outside_home"},
	{CodeConflict, "conflict"},
	{CodeNotConfigured, "not_configured"},

	{CodeObservationEmpty, "observation_empty"},
	{CodeObservationIncomplete, "observation_incomplete"},
	{CodeObservationUnrecognized, "observation_unrecognized"},
	{CodeObservationBlind, "observation_blind"},
	{CodeObservationBindDenied, "observation_bind_denied"},

	{CodeTargetNotListening, "target_not_listening"},
	{CodeShareLimitReached, "share_limit_reached"},
	{CodeListenPortInUse, "listen_port_in_use"},
	{CodeNotSignedIn, "not_signed_in"},
	{CodeShareExpired, "share_expired"},
	{CodeRelayUnreachable, "relay_unreachable"},
	{CodeShareBlocked, "share_blocked"},
	{CodeTargetNotHTTP, "target_not_http"},

	{CodeSessionNotFound, "session_not_found"},
	{CodeClaimConflict, "claim_conflict"},
}

// codeNames is errorRegistry indexed for lookup.
var codeNames = func() map[int]string {
	names := make(map[int]string, len(errorRegistry))
	for _, entry := range errorRegistry {
		names[entry.code] = entry.name
	}
	return names
}()

// CodeName returns the stable string for a numeric code. An unknown code falls
// back to "internal" rather than to an empty string, so a client never has to
// handle a code it cannot branch on.
func CodeName(code int) string {
	if name, known := codeNames[code]; known {
		return name
	}
	return codeNames[CodeInternal]
}

// Errorf builds an error from a format string. It is the primitive: detail and
// the error message are always the same text, so there is one place they could
// drift apart and it is here.
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
