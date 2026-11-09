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
// the difference through the ordinary per-subscriber `include` filter.
//
// Those two numbers are the only parts of a snapshot that change continuously,
// and the port scan they used to ride on is deliberately adaptive: it backs off
// to 5 s while a subscriber watches an unchanging machine, which is right for
// ports and useless for a load meter. Sampling them apart lets the meter run at
// 1 s without pinning the daemon to a full `lsof`/`netstat` scan at that rate.
//
// It parks whenever nothing is subscribed, so an unsubscribed daemon —
// answering `oberth list` and `daemon status` over RPC — samples nothing at
// all.
func (l *Loop) runStats(ctx context.Context) {
	interval := l.statsInterval()
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for ctx.Err() == nil {
		subs, include := l.opts.Demand()
		if subs == 0 {
			// Parked. A read or a new subscriber nudges statsWake; once
			// sampling, the tick keeps its own cadence and ignores it.
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

		armTimer(timer, interval)
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

// statsBasis is what one stats round was sampled against: the snapshot it read
// the pids from, the scan generation that snapshot came from, and the newest
// full stats observation known at capture time. A round that finds either
// counter moved has been overtaken and must not patch newer rows.
type statsBasis struct {
	snap  state.Snapshot
	scan  uint64
	stats uint64
}

// captureStatsBasis takes an immutable input for the round.
func (l *Loop) captureStatsBasis() (statsBasis, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.haveSnap {
		return statsBasis{}, false
	}
	return statsBasis{snap: l.snap, scan: l.committedSeq, stats: l.statsScanSeq}, true
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
	basis, ok := l.captureStatsBasis()
	if !ok || ctx.Err() != nil {
		return
	}
	pids := mergePIDs(statsPIDs(basis.snap.Ports), serviceStatsPIDs(basis.snap.Groups))
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
	prev, next, publish := l.mergeStatsSample(basis, samples)
	if !publish {
		return
	}
	// No lifecycle events and no scan cadence/freshness change. The commit lock
	// still covers commit AND publish, so subscriber sequences cannot invert.
	l.opts.Publish(prev, next, nil)
}

// mergeStatsSample writes a prepared sample into the current snapshot.
//
// It runs under commitMu and inside the fact mutex: the sample is only usable
// while neither the published snapshot nor the full-stats generation moved. A
// newer full stats observation wins outright — no retry, no stale publication,
// no sequence burned. Otherwise a rename, a run replacement or a bare rescan
// advances the snapshot, and the sample is applied to CURRENT rows only, for
// the runtimes it actually observed; the groups already belong to that newer
// scan and must not be patched from an older basis.
func (l *Loop) mergeStatsSample(basis statsBasis, samples map[int]ports.ProcSample) (prev, next state.Snapshot, publish bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	prev = l.snap
	if l.statsScanSeq != basis.stats {
		return state.Snapshot{}, state.Snapshot{}, false
	}
	rebase := prev.Seq != basis.snap.Seq || l.committedSeq != basis.scan

	next = prev
	if rebase {
		next.Ports = refreshStatsFrom(prev.Ports, samples, basis.snap.Ports, true)
	} else {
		next.Ports = refreshStats(prev.Ports, samples)
		next.Groups = refreshGroupStats(prev.Groups, next.Ports, samples)
	}
	if !statsChanged(prev, next) {
		return state.Snapshot{}, state.Snapshot{}, false
	}

	l.seq++
	next.Seq = l.seq
	l.snap = next
	return prev, next, true
}

// statsChanged reports whether any row's or service's stats object moved. The
// slices are index-aligned — next is prev with stats replaced — so this
// compares position by position rather than going through a keyed diff.
func statsChanged(prev, next state.Snapshot) bool {
	if len(prev.Ports) != len(next.Ports) || len(prev.Groups) != len(next.Groups) {
		return true
	}
	for i := range next.Ports {
		if !sameValue(prev.Ports[i].Stats, next.Ports[i].Stats) {
			return true
		}
	}
	for i := range next.Groups {
		if !sameValue(prev.Groups[i].Resources, next.Groups[i].Resources) {
			return true
		}
		if len(prev.Groups[i].Services) != len(next.Groups[i].Services) {
			return true
		}
		for j := range next.Groups[i].Services {
			if !sameValue(prev.Groups[i].Services[j].Stats, next.Groups[i].Services[j].Stats) {
				return true
			}
		}
	}
	return false
}

// sameValue compares two optional values the way a snapshot diff needs: absent
// on both sides is equal, absent on one is a change, and two present values are
// compared by value. It is generic so the two row kinds share one rule rather
// than one copy each.
func sameValue[T comparable](a, b *T) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	}
	return *a == *b
}

// mergePIDs unions two lists with no duplicates. The stats tick samples one
// process once even when a listener row and a service row name the same pid.
func mergePIDs(lists ...[]int) []int {
	seen := make(map[int]bool)
	var out []int
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
	var pids []int
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
			switch {
			case svc.PortActual != nil:
				next = byPort[*svc.PortActual]
			case svc.PID != nil:
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
	var pids []int
	for i := range pp {
		row := &pp[i]
		switch {
		case row.Type == state.TypeDocker, row.PID <= 0, row.Stats == nil:
			continue
		case !state.IsLocalhost(row.Host):
			continue
		}
		pids = append(pids, row.PID)
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
	return refreshStatsAgainst(rows, samples, nil)
}

func refreshStatsFrom(rows []state.Port, samples map[int]ports.ProcSample, basis []state.Port, validate bool) []state.Port {
	if !validate {
		return refreshStatsAgainst(rows, samples, nil)
	}
	return refreshStatsAgainst(rows, samples, basis)
}

// refreshStatsAgainst writes the sampled stats onto the rows that still name
// the same runtime.
//
// basis is the snapshot the sample was taken against. When a publish advanced
// during the IO, the sample belongs to an older generation and must not enrich
// rows that appeared or were replaced since; nil means "the sample is current"
// and skips that check entirely, which is the ordinary tick's path.
func refreshStatsAgainst(rows []state.Port, samples map[int]ports.ProcSample, basis []state.Port) []state.Port {
	if len(samples) == 0 {
		return rows
	}
	var captured map[string]*state.Port
	if basis != nil {
		captured = make(map[string]*state.Port, len(basis))
		for i := range basis {
			captured[basis[i].Key()] = &basis[i]
		}
	}
	// Copy on first actual change, not first sample. Equal samples and samples
	// for absent PIDs allocate nothing; only modified stats objects are owned
	// by the new snapshot. Readers keep the old immutable snapshot.
	out := rows
	copied := false
	for i := range rows {
		row := &out[i]
		if row.Stats == nil || row.Type == state.TypeDocker || !state.IsLocalhost(row.Host) {
			continue
		}
		s, ok := samples[row.PID]
		if !ok || !sampleMatchesRuntime(*row, s) {
			continue
		}
		if captured != nil {
			old, found := captured[row.Key()]
			if !found || !sameRuntime(*old, *row) {
				continue
			}
		}
		st := *row.Stats // copy: the previous snapshot's object is shared
		st.CPUPercent = s.CPUPercent
		st.MemoryRSS = s.MemoryRSS
		st.Uptime = s.Uptime
		st.State = s.State
		if s.ThreadCount > 0 {
			st.ThreadCount = s.ThreadCount
		}
		if st == *row.Stats {
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
