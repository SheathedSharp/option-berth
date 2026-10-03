// Package state holds the daemon's published data model. These structs are the
// JSON wire shape for `oberth list --json`, state.snapshot and state.delta.
// Field names and JSON tags are the contract: docs/schema/protocol.schema.json
// is generated from them and clients in other repositories are built against
// it, so renaming a field here is a breaking protocol change.
package state

import "fmt"

// PortType classifies a listening socket.
type PortType string

const (
	TypeUser   PortType = "user"
	TypeDocker PortType = "docker"
)

// AllPortTypes is the enum used by schema generation.
var AllPortTypes = []PortType{TypeUser, TypeDocker}

// GroupSource records how a port's group was decided. Precedence, highest
// first: start > file > auto (compose / git root).
type GroupSource string

const (
	SourceAuto  GroupSource = "auto"
	SourceFile  GroupSource = "file"
	SourceStart GroupSource = "start"
)

// AllGroupSources is the enum used by schema generation.
var AllGroupSources = []GroupSource{SourceAuto, SourceFile, SourceStart}

// Stats is resource usage for the owning process. Null unless the caller
// opted into stats collection.
type Stats struct {
	CPUPercent  float64 `json:"cpu_percent"`
	MemoryRSS   int64   `json:"memory_rss_bytes"`
	ThreadCount int     `json:"thread_count"`
	Uptime      string  `json:"uptime"`
	State       string  `json:"state"`
	Connections int     `json:"connections"`
}

// Health is the result of an HTTP probe. Null unless health was collected.
//
// Status is exactly one of the contract's three words. Reason carries the
// scanner's finer-grained verdict ("refused", "timeout", "non-http", ...) for
// a client that wants to say *why* a service is failing; it is advisory and
// clients must branch on Status.
type Health struct {
	Status    string `json:"status" jsonschema:"enum=ok,enum=fail,enum=unknown"`
	Code      int    `json:"code"`
	LatencyMs int64  `json:"latency_ms"`
	Reason    string `json:"reason,omitempty"`
	// ObservedAt is when this verdict was obtained, rather than when a
	// snapshot happened to be assembled. It lets clients tell a fresh probe
	// from a carried result without consulting listener internals.
	ObservedAt string `json:"observed_at"`

	// Configured marks a probe the daemon ran because a `oberth.yaml` service
	// declares a `health:` path. Such a row is state, not an opt-in statistic,
	// so it survives the per-subscriber `include` filter. It never goes on the
	// wire: a client sees the health object either way.
	Configured bool `json:"-"`
}

// Health status values (contract §5, §13).
const (
	HealthOK      = "ok"
	HealthFail    = "fail"
	HealthUnknown = "unknown"
)

// NormalizeHealth maps the scanner's probe vocabulary
// ("healthy", "unhealthy", "refused", "timeout", "non-http") onto the three
// words the contract publishes, keeping the original as the reason. It is the
// one place the translation happens, so every published row agrees.
func NormalizeHealth(raw string) (status, reason string) {
	switch raw {
	case "":
		return HealthUnknown, ""
	case HealthOK, "healthy":
		return HealthOK, raw
	case HealthUnknown:
		return HealthUnknown, ""
	case HealthFail:
		return HealthFail, ""
	default:
		return HealthFail, raw
	}
}

// Docker describes the container behind a published port. Null for native
// processes.
type Docker struct {
	Container      string `json:"container"`
	Image          string `json:"image"`
	ComposeService string `json:"compose_service"`
	ComposeProject string `json:"compose_project"`
	ContainerPort  int    `json:"container_port"`
}

// Run attributes a port to a process option-berth started. Null for anything else.
type Run struct {
	ID      string `json:"id"`
	Group   string `json:"group"`
	Name    string `json:"name"`
	RootPID int    `json:"root_pid"`
}

// Session is owned by spec 2; the daemon only carries it.
type Session struct {
	ID       string `json:"id"`
	Tool     string `json:"tool"`
	Label    string `json:"label"`
	Worktree string `json:"worktree"`
	Branch   string `json:"branch"`
	Detected bool   `json:"detected"`
}

// Port is one listening socket as published by the daemon. Clients render
// DisplayName and never derive their own name.
type Port struct {
	// Host is the machine this row was observed on: "localhost" for the
	// daemon's own machine, the registered name for a row multiplexed in from
	// a caller that carries an explicit host tag. It is never empty on the wire.
	Host        string `json:"host"`
	Port        int    `json:"port"`
	BindAddress string `json:"bind_address"`
	// BindAddresses lists the rest of the addresses this one listener holds,
	// present only when there is more than one: one service on one port is one
	// row, and the other address family rides along instead of becoming a row
	// of its own.
	BindAddresses []string `json:"bind_addresses,omitempty"`
	IPVersion     string   `json:"ip_version"`
	URL           string   `json:"url"`
	PID           int      `json:"pid"`
	PPID          int      `json:"ppid"`
	Process       string   `json:"process"`
	DisplayName   string   `json:"display_name"`
	Name          *string  `json:"name" jsonschema:"nullable"`
	Command       string   `json:"command"`
	Cwd           string   `json:"cwd"`
	CwdInTrash    bool     `json:"cwd_in_trash"`
	// CwdGone marks a row whose working directory no longer exists: the process
	// is still holding its port while the project behind it was deleted. It is
	// about whether the path is there, where cwd_in_trash is about where it is.
	CwdGone     bool         `json:"cwd_gone"`
	ProjectRoot *string      `json:"project_root" jsonschema:"nullable"`
	Group       *string      `json:"group" jsonschema:"nullable"`
	GroupSource *GroupSource `json:"group_source" jsonschema:"nullable,enum=auto,enum=file,enum=manual,enum=start"`
	Session     *Session     `json:"session" jsonschema:"nullable"`
	Type        PortType     `json:"type" jsonschema:"enum=user,enum=docker"`
	User        string       `json:"user"`
	Run         *Run         `json:"run" jsonschema:"nullable"`
	Stats       *Stats       `json:"stats" jsonschema:"nullable"`
	Health      *Health      `json:"health" jsonschema:"nullable"`
	Docker      *Docker      `json:"docker" jsonschema:"nullable"`
	StartedAt   *string      `json:"started_at" jsonschema:"nullable"`
}

// Key is the stable delta identity for a port row.
func (p Port) Key() string {
	return PrefixKey(p.Host, fmt.Sprintf("%d:%s", p.Port, p.BindAddress))
}
