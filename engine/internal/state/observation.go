package state

// Observation is what the engine knows about its own view of the listener
// table. It is a fact produced by binding a real socket and looking for it
// with the same collector a status read uses, or by the collector reporting
// that part of its table was unreadable. It is never a guess from environment
// variables.
type Observation struct {
	State     string `json:"state"`
	Code      string `json:"code,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Hint      string `json:"hint,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
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
