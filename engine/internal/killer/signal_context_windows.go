//go:build windows

package killer

import "context"

func signalProcessContext(ctx context.Context, pid int, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validSignalPID(pid) {
		return codedf(CodeInvalidSelector, "", "refusing to signal invalid PID %d", pid)
	}
	return taskkillContext(ctx, killArgs(pid, force, false)...)
}
func signalTreeContext(ctx context.Context, pid int, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validSignalPID(pid) {
		return codedf(CodeInvalidSelector, "", "refusing to signal invalid PID %d", pid)
	}
	return taskkillContext(ctx, killArgs(pid, force, true)...)
}
func signalGroupContext(ctx context.Context, pid int, force bool) error {
	return signalTreeContext(ctx, pid, force)
}
