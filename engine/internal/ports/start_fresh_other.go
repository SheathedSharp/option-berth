//go:build !windows && !linux && !darwin

package ports

import (
	"context"
	"time"
)

func ProcessStartFreshContext(ctx context.Context, pid int) (time.Time, bool) {
	return processStartPSContext(ctx, pid)
}
