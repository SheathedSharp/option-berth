//go:build linux

package ports

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Only the process-independent ELF clock frequency is retained. Birth evidence
// and the boot epoch are reread for each control observation, never cached.
var processClockTicks = sync.OnceValue(func() uint64 {
	entries, err := unix.Auxv()
	if err != nil {
		return 0
	}
	const atClktck = 17
	for _, entry := range entries {
		if entry[0] == atClktck {
			return uint64(entry[1])
		}
	}
	return 0
})

// ProcessStartFreshContext avoids starting a ps process on the normal Linux
// path. The fallback remains bounded and cancellation-owned. Synchronous procfs
// reads cannot be forcibly interrupted; no detached worker is introduced.
func ProcessStartFreshContext(ctx context.Context, pid int) (time.Time, bool) {
	if pid <= 0 || ctx.Err() != nil {
		return time.Time{}, false
	}
	if hz := processClockTicks(); hz > 0 && hz <= 1000000000 {
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if ctx.Err() != nil {
			return time.Time{}, false
		}
		if err == nil {
			boot, err := os.ReadFile("/proc/stat")
			if ctx.Err() != nil {
				return time.Time{}, false
			}
			if err == nil {
				if at, ok := procStartTime(string(stat), string(boot), pid, hz); ok {
					return at, true
				}
			}
		}
	}
	return processStartPSContext(ctx, pid)
}

// comm may contain spaces and closing parentheses. Field 22 is the twentieth
// field after the LAST closing parenthesis, not strings.Fields(stat)[21].
func procStartTime(stat, boot string, pid int, hz uint64) (time.Time, bool) {
	if pid <= 0 || hz == 0 || hz > 1000000000 {
		return time.Time{}, false
	}
	open, close := strings.IndexByte(stat, '('), strings.LastIndexByte(stat, ')')
	if open < 1 || close <= open {
		return time.Time{}, false
	}
	actual, err := strconv.Atoi(strings.TrimSpace(stat[:open]))
	if err != nil || actual != pid {
		return time.Time{}, false
	}
	fields := strings.Fields(stat[close+1:])
	if len(fields) < 20 {
		return time.Time{}, false
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	for _, line := range strings.Split(boot, "\n") {
		key, value, ok := strings.Cut(line, " ")
		if !ok || key != "btime" {
			continue
		}
		epoch, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		const largest = uint64(1<<63 - 1)
		if err != nil || epoch <= 0 || ticks/hz > largest-uint64(epoch) {
			return time.Time{}, false
		}
		sec := epoch + int64(ticks/hz)
		nano := int64((ticks % hz) * 1000000000 / hz)
		return time.Unix(sec, nano), true
	}
	return time.Time{}, false
}
