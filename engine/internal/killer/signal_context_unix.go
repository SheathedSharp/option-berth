//go:build !windows

package killer

import "context"

func signalProcessContext(ctx context.Context, pid int, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return signalProcess(pid, force)
}
func signalGroupContext(ctx context.Context, pid int, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return signalGroup(pid, force)
}
func signalTreeContext(ctx context.Context, pid int, force bool) error {
	return signalProcessContext(ctx, pid, force)
}
