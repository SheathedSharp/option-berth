package workplace

import (
	"errors"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
)

// How a failure out of this package is reported.
//
// The daemon and the CLI both report the same failures — a selector that
// matched nothing, a name already taken — and they must report them the same
// way. The mapping therefore lives here once, next to the errors it classifies,
// rather than in each of the two callers.

// Code is the stable wire code for any error out of this package. Anything
// that is not one of the named failures is internal: a failure nobody
// classified must not be reported as the caller's mistake.
func Code(err error) int {
	switch {
	case errors.Is(err, ErrAmbiguous):
		return rpc.CodeAmbiguous
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrNoWorkplace):
		return rpc.CodeNotFound
	case errors.Is(err, ErrConflict):
		return rpc.CodeConflict
	case errors.Is(err, ErrInvalid):
		return rpc.CodeInvalidParams
	default:
		return rpc.CodeInternal
	}
}

// Hint is the next step for an error out of this package. It is empty when
// there is nothing useful to say, and it never restates the message.
func Hint(err error) string {
	switch {
	case errors.Is(err, ErrAmbiguous):
		return "name the workplace by its full name or id"
	case errors.Is(err, ErrNotFound):
		return "`oberth workplace list` shows the ones this worktree has"
	case errors.Is(err, ErrNoWorkplace):
		return "`oberth workplace new <name>` starts one; running a command with `oberth start` records into one too"
	case errors.Is(err, ErrConflict):
		return "`oberth workplace use <name>` switches to it, or pick another name"
	case errors.Is(err, ErrInvalid):
		return "`oberth workplace record --help` documents the fields"
	default:
		return "the workplace record was left as it was"
	}
}
