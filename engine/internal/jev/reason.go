package jev

// Fixed reasons for "Jev is not available". A caller that has to tell a person
// why nothing happened needs a closed vocabulary, not a provider's error text:
// the same reason goes into the JSON output, the local quality ledger and the
// `oberth jev test` hint, and no branch guesses from a substring.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
)

// ReasonError carries one of the Reason* codes out of the client. Callers use
// errors.As rather than matching on a message.
type ReasonError struct {
	Code   string
	Detail string
	Err    error
}

func (e ReasonError) Error() string {
	if e.Detail == "" {
		return "attention: Jev unavailable: " + e.Code
	}
	return "attention: Jev unavailable: " + e.Code + ": " + e.Detail
}

func (e ReasonError) Unwrap() error {
	if e.Err != nil {
		return e.Err
	}
	return ErrUnavailable
}

// WithReason wraps err so callers can read a fixed code. An empty code is a
// programming error, not a fallthrough: the whole point is that every failure
// path names itself.
func WithReason(code string, err error) error {
	if code == "" {
		code = ReasonProvider
	}
	return ReasonError{Code: code, Err: err}
}

// ReasonFor maps an error to exactly one of the fixed codes. It never returns
// "" — an unclassified failure is provider_error, because calling it "unknown"
// would push the guessing to every caller.
func ReasonFor(err error) string {
	if err == nil {
		return ""
	}
	var reasonErr ReasonError
	if errors.As(err, &reasonErr) && reasonErr.Code != "" {
		return reasonErr.Code
	}
	if errors.Is(err, ErrStale) {
		return ReasonStale
	}
	// An expired artifact is reported as stale too: the answer arrived but no
	// longer describes anything, which for a person is the same as "read again".
	if errors.Is(err, context.DeadlineExceeded) {
		return ReasonTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ReasonTimeout
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return ReasonNetwork
	}
	if errors.Is(err, context.Canceled) {
		return ReasonNetwork
	}
	if errors.Is(err, ErrUnavailable) {
		return ReasonProvider
	}
	return ReasonProvider
}

// ReasonHint is the one-line next step a person can act on. It is written per
// fixed code so a caller never has to phrase a provider's message.
func ReasonHint(code string) string {
	switch code {
	case ReasonNoKey:
		return "run `oberth jev enable` to store a key, or set OPENROUTER_API_KEY / TYPESAFE_API_KEY for this shell"
	case ReasonDisabled:
		return "Jev is off; run `oberth jev enable` (or set BERTH_JEV=1) to allow it for this task"
	case ReasonInvalidConfig:
		return "jev.json is not usable; run `oberth jev status` and then `oberth jev enable` to rewrite it"
	case ReasonUnauthorized:
		return "the provider rejected the key; run `oberth jev enable` to store a current one"
	case ReasonRateLimited:
		return "the provider is rate limiting this key; retry later or switch provider"
	case ReasonTimeout:
		return "the provider did not answer in time; retry, or run `oberth jev test` to check connectivity"
	case ReasonNetwork:
		return "the provider is unreachable from this machine; check the network or proxy, then `oberth jev test`"
	case ReasonBadResponse:
		return "the provider answered in a shape this client cannot validate; run `oberth jev test` for the reason"
	case ReasonStale:
		return "the state changed while the answer was in flight; re-read `oberth status --json` and assess the new event"
	default:
		return "the provider returned an error; run `oberth jev test` for the fixed reason and next step"
	}
}

// reasonFromStatus maps an HTTP status to a fixed code. A status this client
// does not recognise is still classified, never left blank.
func reasonFromStatus(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ReasonUnauthorized
	case status == http.StatusTooManyRequests:
		return ReasonRateLimited
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return ReasonTimeout
	case status >= 500:
		return ReasonProvider
	case status >= 400:
		return ReasonBadResponse
	default:
		return ReasonProvider
	}
}

// reasonFromDecode classifies a response body this client could not accept.
func reasonFromDecode(problem string) string {
	if strings.Contains(problem, "no answers") || strings.Contains(problem, "omitted") ||
		strings.Contains(problem, "invalid") || strings.Contains(problem, "sum to") ||
		strings.Contains(problem, "unauthorized option") {
		return ReasonBadResponse
	}
	return ReasonBadResponse
}

// reasonFromTransport classifies a request that never produced a response.
// A cancelled call is deliberately not "network": the caller's own context
// asked it to stop, and telling the person to check their connection would be a
// wrong next step.
func reasonFromTransport(ctx context.Context, err error) string {
	if errors.Is(err, context.Canceled) {
		return ReasonNetwork
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ReasonTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ReasonTimeout
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return ReasonNetwork
	}
	return ReasonNetwork
}
