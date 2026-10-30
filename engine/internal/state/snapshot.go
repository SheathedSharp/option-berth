package state

// Snapshot is the full published state at one sequence number.
type Snapshot struct {
	Seq           uint64          `json:"seq"`
	At            string          `json:"at"`
	DaemonVersion string          `json:"daemon_version"`
	Ports         []Port          `json:"ports"`
	Groups        []Group         `json:"groups"`
	Sessions      []SessionRecord `json:"sessions"`
	// Observation is present when the listener table is partial or the process
	// cannot trust it. Ports are then facts only for the rows that were read.
	Observation *Observation `json:"observation,omitempty"`
}
