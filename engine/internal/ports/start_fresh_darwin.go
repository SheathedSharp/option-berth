//go:build darwin

package ports

import (
	"context"
	"time"

	"golang.org/x/sys/unix"
)

// ProcessStartFreshContext reads the live process, not the metadata cache.
// sysctl is synchronous; cancellation is checked on either side, not faked by
// detaching a goroutine. Denied visibility falls back to the bounded ps adapter.
func ProcessStartFreshContext(ctx context.Context, pid int) (time.Time, bool) {
	if pid <= 0 || ctx.Err() != nil {
		return time.Time{}, false
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if ctx.Err() != nil {
		return time.Time{}, false
	}
	if err == nil && info != nil && int64(info.Proc.P_pid) == int64(pid) {
		born := info.Proc.P_starttime
		if born.Sec > 0 && born.Usec >= 0 && born.Usec < 1000000 {
			return time.Unix(int64(born.Sec), int64(born.Usec)*1000), true
		}
	}
	return processStartPSContext(ctx, pid)
}
