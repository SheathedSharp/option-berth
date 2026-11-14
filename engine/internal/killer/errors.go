package killer

import (
	"errors"
	"fmt"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// Error codes from the contract's error model (§2). The killer emits only the
// subset it can hit; the runtime service and CLI map them onto their transports.
const (
	// The selector matched nothing.
	CodeNotFound = "not_found"
	// The selector matched several things.
	CodeAmbiguous = "ambiguous"
	// The OS refused the signal.
	CodePermissionDenied = "permission_denied"
	// The selector itself is invalid.
	CodeInvalidSelector = "invalid_selector"
	// The failure was not classified.
	CodeInternal = "internal"
)

// CodedError is a contract code and its hint beside the detail message that
// lands in a Result's Error field.
type CodedError struct {
	Code   string
	Detail string
	Hint   string

	// Err is the underlying cause, when one exists.
	Err error
}

func (coded *CodedError) Error() string { return coded.Detail }
func (coded *CodedError) Unwrap() error { return coded.Err }

func codedf(code, hint, format string, values ...any) *CodedError {
	return &CodedError{Code: code, Hint: hint, Detail: fmt.Sprintf(format, values...)}
}

// permissionErr is the contract's permission_denied with the sudo hint the
// error-handling section requires of kill.
func permissionErr(pid int, cause error) *CodedError {
	return &CodedError{
		Code:   CodePermissionDenied,
		Detail: fmt.Sprintf("not permitted to signal PID %d", pid),
		Hint:   "re-run with sudo",
		Err:    cause,
	}
}

// Code returns the contract error code for err, or "internal" for an error the
// killer did not classify. Returns "" for a nil error.
func Code(err error) string {
	if err == nil {
		return ""
	}
	var coded *CodedError
	if errors.As(err, &coded) {
		return coded.Code
	}
	// A collection failure before any target was resolved carries the
	// collector's code, so a sandbox is not reported as an engine bug.
	var observation *ports.ObservationError
	if errors.As(err, &observation) {
		return observation.Code
	}
	return CodeInternal
}

// Hint returns the actionable hint for err, or "" when there is none.
func Hint(err error) string {
	var coded *CodedError
	if errors.As(err, &coded) {
		return coded.Hint
	}
	var observation *ports.ObservationError
	if errors.As(err, &observation) {
		return observation.Hint
	}
	return ""
}
