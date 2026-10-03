package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/display"
)

// Exit codes — the table in docs/cli.md. A caller branches on the status
// instead of parsing text.
const (
	exitOK          = 0
	exitFail        = 1   // 失败 —— 业务失败也算（找不到端口 / 服务没起来 / 等超时）
	exitUsage       = 2   // 用法错 —— flag 或参数不合法
	exitInterrupted = 130 // 128 + SIGINT —— 被 Ctrl+C 打断
)

// jsonMode records whether this invocation asked for JSON. Each command owns
// its own --json flag, but the error is rendered in Execute after the command
// has returned, so the answer has to be captured in PersistentPreRun.
var jsonMode bool

// usageError marks "the caller used the command wrong", so Execute can exit 2
// rather than 1. pflag's parse errors have no exported type, so cobra's
// SetFlagErrorFunc wraps them in this; commands may return it directly for
// their own argument checks.
type usageError struct{ err error }

func (u usageError) Error() string { return u.err.Error() }
func (u usageError) Unwrap() error { return u.err }

// cliErr is an error carrying a stable code from docs/cli.md. Errors without
// one are classified by describe().
type cliErr struct {
	Code string
	Msg  string
	Hint string
}

func (e *cliErr) Error() string { return e.Msg }

// fail builds an error with an explicit code.
func fail(code, format string, a ...any) error {
	return &cliErr{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// failHint is fail with a hint: the line a caller can act on.
func failHint(code, msg, hint string) error {
	return &cliErr{Code: code, Msg: msg, Hint: hint}
}

// cliError turns a daemon error into one this package can render. It keeps the
// wire's own stable code (contract §2: clients branch on error.data.code, never
// on the numeric JSON-RPC code) and the hint the daemon attached.
func cliError(err error) error {
	if err == nil {
		return nil
	}
	var re *rpc.Error
	if !errors.As(err, &re) {
		return err
	}
	return &cliErr{Code: re.Data.Code, Msg: re.Data.Detail, Hint: re.Data.Hint}
}

// describe splits an error into the three things both renderings need.
func describe(err error) (code, msg, hint string) {
	var ce *cliErr
	if errors.As(err, &ce) {
		return ce.Code, ce.Msg, ce.Hint
	}
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Data.Code, re.Data.Detail, re.Data.Hint
	}
	var ue usageError
	if errors.As(err, &ue) {
		return "invalid_params", ue.Error(), "run `oberth --help` for usage"
	}
	if errors.Is(err, context.Canceled) {
		return "interrupted", "interrupted", ""
	}
	return "internal", err.Error(), ""
}

// exitCodeFor maps an error to the documented status.
func exitCodeFor(err error) int {
	var ue usageError
	if errors.As(err, &ue) {
		return exitUsage
	}
	if errors.Is(err, context.Canceled) {
		return exitInterrupted
	}
	return exitFail
}

// errorLine renders err the way reportError does, as one string — for the
// places that print a daemon error themselves instead of returning it.
func errorLine(err error) string {
	_, msg, hint := describe(err)
	if hint == "" {
		return msg
	}
	return msg + "\nhint: " + hint
}

// reportError renders err on w and returns the exit status Execute should use.
//
// stdout is left alone: docs/cli.md promises a caller that in `--json` mode
// stdout holds exactly one JSON value, so everything else — this included —
// goes to stderr.
func reportError(w io.Writer, err error) int {
	code := exitCodeFor(err)
	if errors.Is(err, errSilent) {
		// The command already reported it (in JSON, usually). Non-zero, quiet.
		return code
	}

	c, msg, hint := describe(err)
	if jsonMode {
		body := map[string]any{"code": c, "message": msg}
		if hint != "" {
			body["hint"] = hint
		}
		_ = encodeJSON(w, map[string]any{"error": body})
		return code
	}
	fmt.Fprintf(w, "%s %s\n", display.Red("error:"), msg)
	if hint != "" {
		fmt.Fprintf(w, "%s %s\n", display.Dim("hint:"), hint)
	}
	return code
}
