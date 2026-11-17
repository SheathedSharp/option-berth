package state

import "slices"

// Observation is what the engine knows about its own view of the listener
// table. It is a fact produced by binding a real socket and looking for it
// with the same collector a status read uses, or by the collector reporting
// that part of its table was unreadable. It is never a guess from environment
// variables.
type Observation struct {
	State string `json:"state"`
	Code  string `json:"code,omitempty"`
	// Source names the one collector that answered when it was not the
	// platform's primary one ("lsof" on Darwin, "netstat" on Linux). It is the
	// single-table counterpart of Sources: exactly one of the two is set, and
	// either of them means the table can promise less than the primary one, so
	// a port missing from it is unknown rather than free. Empty means the
	// primary collector answered alone.
	Source string `json:"source,omitempty"`
	// Sources names every collector that contributed to a merged table, in the
	// order the platform's chain reads them. A merged table is never the
	// platform's own answer, so it is always narrow: the rows in it are facts,
	// and every port missing from it is unknown. Absent for a single-collector
	// read, which says which one answered in Source.
	Sources []string `json:"sources,omitempty"`
	Detail  string   `json:"detail,omitempty"`
	Hint    string   `json:"hint,omitempty"`
	// CheckedAt is when this scan looked. It is the moment of the check, not
	// the beginning of the condition it describes: a re-check of the same
	// verdict moves it, and it says nothing about how long the condition has
	// lasted.
	CheckedAt string `json:"checked_at,omitempty"`
	// DegradedSince is when the currently published degradation began: the
	// first verdict that was not a whole read of the primary table. It stays
	// fixed for as long as that degradation lasts, so a reader can tell a
	// just-noticed problem from one that has been going on. The runtime service
	// maintains it; a direct read has no history and leaves it empty.
	DegradedSince string `json:"degraded_since,omitempty"`
	// RecoveredAt is when the current healthy run began after a degradation.
	// It is set together with clearing DegradedSince, and stays until the next
	// degradation, so "healthy since T" survives the re-checks that follow.
	// The runtime service maintains it; a direct read has no history and leaves
	// it empty.
	RecoveredAt string `json:"recovered_at,omitempty"`
}

// Narrow reports whether the table this observation describes cannot cover
// every socket owner. A narrow table is still usable for what it shows: a row
// in it is a fact, and only absence is unknown. A merged table is narrow
// however many collectors contributed to it, because none of them alone
// carries the primary table's authority.
func (o *Observation) Narrow() bool {
	return o != nil && (o.Source != "" || len(o.Sources) > 0)
}

// SameObservation compares two verdicts without treating a re-checked healthy
// process as changed: CheckedAt is the moment of the probe, not a fact about
// the table, so a process that keeps seeing itself publishes nothing new.
//
// The two times are excluded for the same reason: they travel with the state
// change that produced them, so comparing them would turn every tick into a
// new frame without saying anything new.
func SameObservation(a, b *Observation) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.State == b.State && a.Code == b.Code && a.Source == b.Source &&
		sameSources(a.Sources, b.Sources) &&
		a.Detail == b.Detail && a.Hint == b.Hint
}

// sameSources compares two contributor lists as sets of names. The order the
// platform's chain reads them in is not a fact about this machine, so a merge
// that produced the same contributors in another order has not changed.
func sameSources(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	sortedA, sortedB := slices.Clone(a), slices.Clone(b)
	slices.Sort(sortedA)
	slices.Sort(sortedB)
	return slices.Equal(sortedA, sortedB)
}

const (
	// ObservationObservable: the collector saw the process's own listener.
	ObservationObservable = "observable"
	// ObservationPartial: the collector read a table, but some rows were
	// unreadable. The readable rows are facts; the unreadable ports are unknown.
	ObservationPartial = "partial"
	// ObservationBlind: the process cannot see its own listener.
	ObservationBlind = "blind"
	// ObservationBindDenied: the process cannot bind a local port at all.
	ObservationBindDenied = "bind_denied"
)
