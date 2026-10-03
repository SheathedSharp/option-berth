package ports

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// displaySignalCache keeps the expensive process-table and cwd lookups out of
// every unchanged scan. lsof still discovers listeners on every pass; this
// cache only covers metadata that is stable for a running PID. A new PID that
// is not in the cached table forces an immediate refresh, so a newly started
// service is never hidden behind the TTL.
const displaySignalCacheTTL = 5 * time.Second

var displaySignalCache struct {
	sync.Mutex
	seq     uint64
	at      time.Time
	covered map[int]struct{}
	pidInfo map[int]pidEntry
	cwds    map[int]string
}

// enrichDisplayNameSignals batches the I/O needed by resolveProcessName:
// process identity (command, parent, start time) and working directories.
// All lookups are batched into a small number of subprocess calls so that
// the overhead is bounded even with many listening ports. On Unix the same
// cached table is the only source for a row's command and started_at, so an
// unchanged scan costs no `ps` for identity at all.
func enrichDisplayNameSignals(pp []ListeningPort) {
	_ = enrichDisplayNameSignalsContext(context.Background(), pp)
}

func enrichDisplayNameSignalsContext(ctx context.Context, pp []ListeningPort) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(pp) == 0 {
		return nil
	}
	pids := collectPIDs(pp)
	if len(pids) == 0 {
		return nil
	}

	// 1. Process table + cwd lookups. These are cached briefly because the
	//    listener scan below already detects new PIDs. A cache miss batches
	//    both calls exactly as before.
	pidInfo, cwds, err := cachedDisplayNameSignalsContext(ctx, pids)
	if err != nil {
		return err
	}
	applyDisplayNameSignals(pp, pidInfo, cwds)
	return ctx.Err()
}

// applyDisplayNameSignals only joins observed facts. Missing process/cwd
// evidence does not remove a listener or invent ownership and timestamps.
func applyDisplayNameSignals(pp []ListeningPort, pidInfo map[int]pidEntry, cwds map[int]string) {
	// 2. Process identity only: which run owns a listener is an attribution
	//    decision and is made by the attribution layer (groups), which holds
	//    the run registry. The collection layer reports what the OS shows.
	for i := range pp {
		pid := pp[i].PID
		if pid <= 0 {
			continue
		}
		if info, ok := pidInfo[pid]; ok {
			if info.ppid > 0 {
				pp[i].PPID = info.ppid
			}
			// On Windows these were already filled from the CIM query; the
			// Unix table is the only source for them, so an empty field means
			// this table is where it comes from.
			if pp[i].Command == "" {
				pp[i].Command = info.cmd
			}
			if pp[i].StartedAt == "" && info.startedAt != "" {
				pp[i].StartedAt = parseStartTime(info.startedAt)
			}
			if info.ppid > 1 {
				if parent, ok := pidInfo[info.ppid]; ok {
					pp[i].ParentCmd = parent.cmd
				}
			}
		}
		if cwd, ok := cwds[pid]; ok {
			pp[i].Cwd = cwd
		}
	}

	// Project root and group attribution are not done here: they belong to
	// internal/groups, which runs the full precedence chain (pin, run, config,
	// Compose, git root) over the finished scan.
}

// cachedDisplayNameSignals returns immutable maps owned by the cache. Refreshes
// replace the maps wholesale, so readers that already received a cache hit can
// safely finish without holding the mutex.
func cachedDisplayNameSignals(pids []int) (map[int]pidEntry, map[int]string) {
	info, cwds, _ := cachedDisplayNameSignalsContext(context.Background(), pids)
	return info, cwds
}

func cachedDisplayNameSignalsContext(ctx context.Context, pids []int) (map[int]pidEntry, map[int]string, error) {
	return readDisplayNameSignals(ctx, pids, batchGetProcessTableContext, batchGetCwdsContext)
}

// readDisplayNameSignals stages one metadata observation before cache admission.
// The two synchronous adapters are arguments, not mutable global hooks; tests
// can place cancellation exactly at the boundary between process and cwd IO.
func readDisplayNameSignals(ctx context.Context, pids []int,
	table func(context.Context) map[int]pidEntry,
	directories func(context.Context, []int) map[int]string,
) (map[int]pidEntry, map[int]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	now := time.Now()
	displaySignalCache.Lock()
	if err := ctx.Err(); err != nil {
		displaySignalCache.Unlock()
		return nil, nil, err
	}
	if !displaySignalCache.at.IsZero() && !now.Before(displaySignalCache.at) &&
		now.Sub(displaySignalCache.at) < displaySignalCacheTTL &&
		cachedSignalsCover(pids, displaySignalCache.covered, displaySignalCache.pidInfo) {
		info, cwds := displaySignalCache.pidInfo, displaySignalCache.cwds
		displaySignalCache.Unlock()
		return info, cwds, nil
	}
	observed := beginProcessObservation()
	displaySignalCache.Unlock()

	info := table(ctx)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	cwds := directories(ctx, pids)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	// Partial identity is useful now, but cannot claim coverage of missing PIDs.
	// All IO is finished before either cache is admitted. Cancellation after
	// this checkpoint is not a rollback of an already completed observation.
	var covered map[int]struct{}
	if cachedSignalsCover(pids, nil, info) {
		covered = make(map[int]struct{}, len(pids))
		for _, pid := range pids {
			covered[pid] = struct{}{}
		}
	}
	displaySignalCache.Lock()
	defer displaySignalCache.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if observed.seq > displaySignalCache.seq {
		displaySignalCache.seq = observed.seq
		// A partial newer table can contradict cached rows. Keep returned
		// immutable maps alive, but revoke the old cache's coverage rather
		// than later reporting those contradicted identities as fresh.
		displaySignalCache.at = time.Time{}
		if covered != nil {
			displaySignalCache.at = observed.at
			displaySignalCache.covered = covered
			displaySignalCache.pidInfo = info
			displaySignalCache.cwds = cwds
		}
	}
	rememberObservedParentEntries(info, observed)
	return info, cwds, nil
}

func cachedSignalsCover(pids []int, covered map[int]struct{}, info map[int]pidEntry) bool {
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if covered != nil {
			if _, ok := covered[pid]; !ok {
				return false
			}
		}
		// Windows has no ps -A table here; its PID metadata comes from the
		// CIM query in Enrich and the cache is still useful for cwd lookups.
		if runtime.GOOS != "windows" {
			if _, ok := info[pid]; !ok {
				return false
			}
		}
	}
	return true
}

type pidEntry struct {
	ppid      int
	cmd       string
	startedAt string // raw ps lstart; parsed only when a listener row needs it
}

// batchGetProcessTable returns a pid -> {ppid, command, start time} map for
// every process the current user can see, via a single ps call. The table is
// fetched once per displaySignalCache refresh and carries everything the
// always-on enrichment needs, so an unchanged scan runs no per-pid ps at all.
func batchGetProcessTable() map[int]pidEntry {
	observed := beginProcessObservation()
	result := batchGetProcessTableContext(context.Background())
	rememberObservedParentEntries(result, observed)
	return result
}

func batchGetProcessTableContext(ctx context.Context) map[int]pidEntry {
	result := make(map[int]pidEntry)
	if runtime.GOOS == "windows" || ctx.Err() != nil {
		return result // not supported via this path on Windows
	}
	cmd, stop := boundedContext(ctx, "ps", "-A", "-o", "pid=,ppid=,lstart=,command=")
	defer stop()
	out, err := cmd.Output()
	if err != nil || ctx.Err() != nil {
		return result
	}
	result = parseProcessTable(string(out))
	return result
}

// lstartFields is how many whitespace-separated tokens `ps -o lstart=` emits:
// "Mon Jan  2 15:04:05 2006".
const lstartFields = 5

// parseProcessTable reads `ps -A -o pid=,ppid=,lstart=,command=` output. The
// command is everything after the fixed-width prefix, so it may contain any
// whitespace; lstart's five tokens sit between the parent pid and it.
func parseProcessTable(out string) map[int]pidEntry {
	result := make(map[int]pidEntry)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2+lstartFields+1 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil || ppid < 0 {
			continue
		}
		cmd := restAfterFields(line, 2+lstartFields)
		if cmd == "" {
			continue
		}
		result[pid] = pidEntry{ppid: ppid, cmd: cmd, startedAt: strings.Join(fields[2:2+lstartFields], " ")}
	}
	return result
}

// restAfterFields returns line with its first n whitespace-separated fields
// removed, preserving the remainder verbatim.
func restAfterFields(line string, n int) string {
	rest := line
	for i := 0; i < n; i++ {
		rest = strings.TrimLeft(rest, " \t")
		idx := strings.IndexAny(rest, " \t")
		if idx < 0 {
			return ""
		}
		rest = rest[idx:]
	}
	return strings.TrimLeft(rest, " \t")
}
