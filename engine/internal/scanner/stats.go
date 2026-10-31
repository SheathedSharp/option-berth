package scanner

import (
	"context"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// runStats is the stats-only tick: a fixed sampler, decoupled from the port
// scan, that refreshes each listening process's cpu and memory and publishes
// the difference
// through the ordinary per-subscriber `include` filter.
//
// It exists because those two things are the only parts of a snapshot that
// change continuously, and the port scan they used to ride on is deliberately
// adaptive: it backs off to 5 s while a subscriber watches an unchanging
// machine (contract §37), which is right for ports and useless for a load
// meter. Sampling them apart lets the meter run at 1 s without pinning the
// daemon to a full `lsof`/`netstat` scan at the same rate.
//
// It parks whenever nothing is subscribed, so an unsubscribed daemon —
// answering `oberth list` and `daemon status` over RPC — samples nothing at
// all.
func (l *Loop) runStats(ctx context.Context) {
	interval := l.statsInterval()
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		if ctx.Err() != nil {
			return
		}
		subs, include := l.opts.Demand()
		if subs == 0 {
			select {
			case <-ctx.Done():
				return
			case <-l.statsWake:
				continue
			}
		}

		round, cancel := observationContext(ctx)
		l.sampleStatsContext(round, include)
		cancel()

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(interval)
		// Deliberately not woken by l.statsWake here: a read or a new
		// subscriber must not make the sampler fire faster than its own
		// cadence. Only a parked sampler listens for the nudge.
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

// statsInterval is the cadence, clamped to something a machine can sustain.
func (l *Loop) statsInterval() time.Duration {
	d := l.opts.StatsInterval
	if d <= 0 {
		return StatsInterval
	}
	if d < MinStatsInterval {
		return MinStatsInterval
	}
	return d
}

// sampleStats runs one stats-only tick and publishes what moved.
//
// It never touches the scan interval, the scan counters or `lastScanAt`: the
// port scan's adaptive cadence, the RPC cache TTL and `daemon status` all read
// exactly what they read before this tick existed.
func (l *Loop) sampleStats(include Include) {
	l.sampleStatsContext(context.Background(), include)
}

func (l *Loop) sampleStatsContext(ctx context.Context, include Include) {
	if !include.Stats || ctx.Err() != nil {
		return
	}
	if err := l.statsMu.LockContext(ctx); err != nil {
		return
	}
	defer l.statsMu.Unlock()

	// Capture an immutable input, then do OS work without blocking commits.
	l.mu.Lock()
	basis, have := l.snap, l.haveSnap
	scan, statsScan := l.committedSeq, l.statsScanSeq
	l.mu.Unlock()
	if !have || ctx.Err() != nil {
		return
	}
	pids := mergePIDs(statsPIDs(basis.Ports), serviceStatsPIDs(basis.Groups))
	var samples map[int]ports.ProcSample
	if l.opts.SampleStats != nil {
		samples = l.opts.SampleStats(pids)
	} else {
		samples = ports.SampleProcStatsContext(ctx, pids)
	}
	if len(samples) == 0 || ctx.Err() != nil {
		return
	}

	if err := l.commitMu.LockContext(ctx); err != nil {
		return
	}
	defer l.commitMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	l.mu.Lock()
	prev := l.snap
	newStatsScan := l.statsScanSeq != statsScan
	rebase := prev.Seq != basis.Seq || l.committedSeq != scan
	l.mu.Unlock()
	if newStatsScan {
		// A newer full stats observation has already won. No retry, stale
		// publication, sequence consumption or extra OS call is needed.
		return
	}

	next := prev
	if rebase {
		// A rename, run replacement or bare rescan can progress while sampling.
		// Patch CURRENT rows only, and only the runtimes the sample observed.
		// The groups belong to that newer scan and already carry its samples;
		// patching them from this older basis would cross generations.
		next.Ports = refreshStatsFrom(prev.Ports, samples, basis.Ports, true)
	} else {
		next.Ports = refreshStats(prev.Ports, samples)
		next.Groups = refreshGroupStats(prev.Groups, next.Ports, samples)
	}
	if !statsChanged(prev, next) {
		return
	}

	l.mu.Lock()
	l.seq++
	next.Seq = l.seq
	l.snap = next
	l.mu.Unlock()
	// No lifecycle events and no scan cadence/freshness change. The mutex
	// still covers commit AND publish, so subscriber sequences cannot invert.
	l.opts.Publish(prev, next, nil)
}

// statsChanged reports whether any row's or service's stats object moved. The
// slices are index-aligned — next is prev with stats replaced — so this
// compares position by position rather than going through a keyed diff.
func statsChanged(prev, next state.Snapshot) bool {
	if len(prev.Ports) != len(next.Ports) || len(prev.Groups) != len(next.Groups) {
		return true
	}
	for i := range next.Ports {
		if !sameStats(prev.Ports[i].Stats, next.Ports[i].Stats) {
			return true
		}
	}
	for i := range next.Groups {
		if !sameResources(prev.Groups[i].Resources, next.Groups[i].Resources) {
			return true
		}
		if len(prev.Groups[i].Services) != len(next.Groups[i].Services) {
			return true
		}
		for j := range next.Groups[i].Services {
			if !sameStats(prev.Groups[i].Services[j].Stats, next.Groups[i].Services[j].Stats) {
				return true
			}
		}
	}
	return false
}

func sameStats(a, b *state.Stats) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

func sameResources(a, b *state.Resources) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

// mergePIDs unions two lists with no duplicates. The stats tick samples one
// process once even when a listener row and a service row name the same pid.
func mergePIDs(lists ...[]int) []int {
	seen := map[int]bool{}
	out := make([]int, 0)
	for _, pids := range lists {
		for _, pid := range pids {
			if pid <= 0 || seen[pid] {
				continue
			}
			seen[pid] = true
			out = append(out, pid)
		}
	}
	return out
}

// serviceStatsPIDs are the run root processes of running services that have no
// listener. A listener's process is already in statsPIDs; sampling it twice
// would only cost a duplicate entry in one ps call.
func serviceStatsPIDs(groups []state.Group) []int {
	pids := make([]int, 0, len(groups))
	for i := range groups {
		for j := range groups[i].Services {
			svc := &groups[i].Services[j]
			if !svc.Running || svc.PortActual != nil || svc.PID == nil || *svc.PID <= 0 {
				continue
			}
			pids = append(pids, *svc.PID)
		}
	}
	return pids
}

// refreshGroupStats writes the sampled stats back onto each service. A service
// with a listener takes the sample from its listener row, so the port row and
// the service row cannot disagree; a portless worker takes its own pid's
// sample. Group totals are recomputed after the service rows are in place.
func refreshGroupStats(rows []state.Group, ports []state.Port, samples map[int]ports.ProcSample) []state.Group {
	if len(rows) == 0 || len(samples) == 0 {
		return rows
	}
	byPort := make(map[int]*state.Stats, len(ports))
	for i := range ports {
		if ports[i].Stats != nil {
			byPort[ports[i].Port] = ports[i].Stats
		}
	}

	out := rows
	copied := false
	for i := range rows {
		changed := false
		for j := range rows[i].Services {
			svc := &rows[i].Services[j]
			if !svc.Running {
				continue
			}
			var next *state.Stats
			if svc.PortActual != nil {
				next = byPort[*svc.PortActual]
			} else if svc.PID != nil {
				if s, ok := samples[*svc.PID]; ok {
					stats := state.Stats{
						CPUPercent: s.CPUPercent, MemoryRSS: s.MemoryRSS,
						ThreadCount: s.ThreadCount, Uptime: s.Uptime, State: s.State,
					}
					next = &stats
				}
			}
			if next == nil || (svc.Stats != nil && *svc.Stats == *next) {
				continue
			}
			if !copied {
				out = append([]state.Group(nil), rows...)
				copied = true
			}
			if !changed {
				out[i].Services = append([]state.Service(nil), rows[i].Services...)
				changed = true
			}
			stats := *next
			out[i].Services[j].Stats = &stats
		}
	}
	if copied {
		groups.FinalizeResources(out)
	}
	return out
}

// statsPIDs are the processes a stats tick samples, minus containers whose
// stats come from Docker. A pid that has since exited is simply missing.
func statsPIDs(pp []state.Port) []int {
	pids := make([]int, 0, len(pp))
	for i := range pp {
		if pp[i].Type == state.TypeDocker || pp[i].PID <= 0 || pp[i].Stats == nil {
			continue
		}
		if !state.IsLocalhost(pp[i].Host) {
			continue
		}
		pids = append(pids, pp[i].PID)
	}
	return pids
}

// refreshStats returns the given rows with only their `stats` object replaced.
// Every other field is carried through untouched, which makes a stats delta
// invisible to a subscriber that did not ask for stats.
//
// A row the sample has nothing to say about keeps the stats it had: a pid that
// vanished between the scan and this tick is dropped from the sample, not
// zeroed on the wire, and the next port scan is what removes the row.
// `connections` is carried forward too — counting them is an `lsof` (or `ss`,
// or `netstat`) per port and stays on the scan tick.
func refreshStats(rows []state.Port, samples map[int]ports.ProcSample) []state.Port {
	return refreshStatsFrom(rows, samples, nil, false)
}

// A rebased sample must not enrich newly discovered or replacement runtimes.
// The index is temporary and only built when publication advanced during I/O;
// the ordinary stats tick retains its allocation-free unchanged-row path.
func refreshStatsFrom(rows []state.Port, samples map[int]ports.ProcSample, basis []state.Port, validate bool) []state.Port {
	if len(samples) == 0 {
		return rows
	}
	var captured map[string]*state.Port
	if validate {
		captured = make(map[string]*state.Port, len(basis))
		for i := range basis {
			captured[basis[i].Key()] = &basis[i]
		}
	}
	// Copy on first actual change, not first sample. Equal samples and samples
	// for absent PIDs allocate nothing; only modified stats objects are owned
	// by the new snapshot. Readers can retain the old immutable snapshot.
	out := rows
	copied := false
	for i := range rows {
		if out[i].Stats == nil || out[i].Type == state.TypeDocker || !state.IsLocalhost(out[i].Host) {
			continue
		}
		s, ok := samples[out[i].PID]
		if !ok || !sampleMatchesRuntime(out[i], s) {
			continue
		}
		if validate {
			old, found := captured[out[i].Key()]
			if !found || !sameRuntime(*old, out[i]) {
				continue
			}
		}
		st := *out[i].Stats // copy: the previous snapshot's object is shared
		st.CPUPercent = s.CPUPercent
		st.MemoryRSS = s.MemoryRSS
		st.Uptime = s.Uptime
		st.State = s.State
		if s.ThreadCount > 0 {
			st.ThreadCount = s.ThreadCount
		}
		if st == *rows[i].Stats {
			continue
		}
		if !copied {
			out = append([]state.Port(nil), rows...)
			copied = true
		}
		updated := st
		out[i].Stats = &updated
	}
	return out
}

// Even without a new published scan, the OS may already have recycled this
// PID. Snapshot-to-snapshot validation alone cannot detect that race. Missing
// identity remains unknown, but contradictory positive birth times forbid a
// patch. Offset spelling differences do not change the process identity.
func sampleMatchesRuntime(row state.Port, sample ports.ProcSample) bool {
	return row.StartedAt == nil || sample.MatchesStart(*row.StartedAt)
}
