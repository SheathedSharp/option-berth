//go:build windows

package ports

import (
	"context"
	"syscall"
	"time"
)

// ProcessStartFreshContext queries a kernel process handle instead of a cached
// CIM table or starting PowerShell for each signal. Calls are synchronous;
// cancellation is checked at their boundaries and the handle is always closed.
func ProcessStartFreshContext(ctx context.Context, pid int) (time.Time, bool) {
	if pid <= 0 || uint64(pid) > 1<<32-1 || ctx.Err() != nil {
		return time.Time{}, false
	}
	const queryLimitedInformation = 0x1000
	h, err := syscall.OpenProcess(queryLimitedInformation, false, uint32(pid))
	if err != nil {
		return time.Time{}, false
	}
	defer syscall.CloseHandle(h)
	var created, exited, kernel, user syscall.Filetime
	err = syscall.GetProcessTimes(h, &created, &exited, &kernel, &user)
	if err != nil || ctx.Err() != nil || (created.HighDateTime == 0 && created.LowDateTime == 0) {
		return time.Time{}, false
	}
	return time.Unix(0, created.Nanoseconds()), true
}
