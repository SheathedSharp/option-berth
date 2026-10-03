//go:build !windows

package ports

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// processStartPSContext is the bounded fallback for live birth evidence.
// Unlike ProcessStart it neither reads nor warms any cross-observation cache.
// A missing/failed query is unknown. The caller decides whether to refuse.
func processStartPSContext(ctx context.Context, pid int) (time.Time, bool) {
	if pid <= 0 || ctx.Err() != nil {
		return time.Time{}, false
	}
	cmd, cancel := boundedContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "lstart=")
	defer cancel()
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil || ctx.Err() != nil {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, parseStartTime(strings.TrimSpace(string(out))))
	return at, err == nil && !at.IsZero()
}
