package scanner

import (
	"context"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
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
	pids := statsPIDs(basis.Ports)
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
		next.Ports = refreshStatsFrom(prev.Ports, samples, basis.Ports, true)
	} else {
		next.Ports = refreshStats(prev.Ports, samples)
	}
	if !statsChanged(prev.Ports, next.Ports) {
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

// statsChanged reports whether any row's stats object moved. The two slices
// are index-aligned — next is prev with stats replaced — so this compares
// position by position rather than going through a keyed diff.
func statsChanged(prev, next []state.Port) bool {
	if len(prev) != len(next) {
		return true
	}
	for i := range next {
		a, b := prev[i].Stats, next[i].Stats
		switch {
		case a == nil && b == nil:
		case a == nil || b == nil:
			return true
		case *a != *b:
			return true
		}
	}
	return false
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
