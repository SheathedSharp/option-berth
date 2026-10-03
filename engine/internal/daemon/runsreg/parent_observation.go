package runsreg

// parentTable reuses one immutable parent observation for parentsTTL.
// That bounds this registry's reuse, not the age of an upstream cache.
// System calls stay outside mu; concurrent misses may collect independently.
// Tickets order accepted observations, not completion clocks. No waiting worker
// or second registry is introduced.
func (r *Registry) parentTable() map[int]int {
	r.mu.Lock()
	now := r.clock()
	fresh := r.parents != nil && !r.parentsAt.IsZero() &&
		!now.Before(r.parentsAt) && now.Sub(r.parentsAt) < parentsTTL
	if fresh {
		table := r.parents
		r.mu.Unlock()
		return table
	}
	load := r.Parents
	if load == nil {
		r.mu.Unlock()
		return nil // a disabled reader cannot renew expired evidence
	}
	r.parentsSeq++
	seq := r.parentsSeq
	r.mu.Unlock()

	table := load()
	r.mu.Lock()
	if seq > r.parentsCommitted {
		r.parentsCommitted = seq
		// Age begins before I/O. A slow read does not get a new TTL when it
		// finishes, and a later empty observation prevents an older read
		// from resurrecting superseded evidence.
		r.parents, r.parentsAt = table, now
	}
	r.mu.Unlock()
	// This call may use its own observation. Only the shared cache obeys the
	// accepted-ticket order; this is not an atomic OS snapshot or PID handle.
	return table
}
