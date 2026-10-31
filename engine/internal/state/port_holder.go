package state

// PortHolderKind says how the process holding a service's declared port is
// related to option-berth.
type PortHolderKind string

const (
	// PortHolderRun is a run option-berth started, for this or another
	// worktree. Group, Name and RunID say which.
	PortHolderRun PortHolderKind = "oberth_run"
	// PortHolderPrevious is a previous generation of the same service still
	// holding the port. RunID reveals whether it is the failed run's own child.
	PortHolderPrevious PortHolderKind = "previous_run"
	// PortHolderListener is a listener attributed to a worktree without a
	// managed run owning it. Whether a manifest declares it is not knowable
	// from the scan.
	PortHolderListener PortHolderKind = "worktree_listener"
	// PortHolderUnknown is a listener with no worktree attribution.
	PortHolderUnknown PortHolderKind = "unknown"
)

// AllPortHolderKinds is the enum used by schema generation.
var AllPortHolderKinds = []PortHolderKind{
	PortHolderRun, PortHolderPrevious, PortHolderListener, PortHolderUnknown,
}

// PortHolder is the process observed holding a service's declared port when
// that service could not bind it. It is evidence for one port_occupied exit,
// scoped to the one port the service named — not a machine-wide port list.
//
// It is a read-time join of a run exit fact with the listener batch from the
// same observation. It is not persisted: absence means no holder was observed
// at read time, never that the port is free.
type PortHolder struct {
	Port        int    `json:"port"`
	BindAddress string `json:"bind_address,omitempty"`
	PID         int    `json:"pid"`
	// Command is the holder's command line, the fact a reader needs before
	// deciding whether it may stop it. It can be empty when the process table
	// was unreadable; PID and Kind still stand.
	Command string          `json:"command"`
	Cwd     string          `json:"cwd,omitempty"`
	Kind    PortHolderKind  `json:"kind" jsonschema:"enum=oberth_run,enum=previous_run,enum=worktree_listener,enum=unknown"`
	Group   string          `json:"group,omitempty"`
	Name    string          `json:"name,omitempty"`
	RunID   string          `json:"run_id,omitempty"`
}

// samePortHolder compares two holders by value. A pointer comparison would
// treat two publications of the same facts as different whenever the resolver
// allocates a fresh holder, which every scan does.
func samePortHolder(a, b *PortHolder) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
