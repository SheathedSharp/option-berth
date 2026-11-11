package state

// Observation is what the engine knows about its own view of the listener
// table. It is a fact produced by binding a real socket and looking for it
// with the same collector a status read uses, or by the collector reporting
// that part of its table was unreadable. It is never a guess from environment
// variables.
type Observation struct {
	State string `json:"state"`
	Code  string `json:"code,omitempty"`
	// Source names the collector that answered when it was not the platform's
	// primary one ("lsof" on Darwin). A non-empty source means the table is
	// narrow: it covers this user's sockets, so a port missing from it is
	// unknown rather than free. Empty means the primary collector answered.
	Source    string `json:"source,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Hint      string `json:"hint,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
}

// Narrow reports whether the table this observation describes cannot cover
// every socket owner. A narrow table is still usable for what it shows: a row
// in it is a fact, and only absence is unknown.
func (o *Observation) Narrow() bool {
	return o != nil && o.Source != ""
}

// SameObservation compares two verdicts without treating a re-checked healthy
// process as changed: CheckedAt is the moment of the probe, not a fact about
// the table, so a process that keeps seeing itself publishes nothing new.
func SameObservation(a, b *Observation) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.State == b.State && a.Code == b.Code && a.Source == b.Source &&
		a.Detail == b.Detail && a.Hint == b.Hint
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
