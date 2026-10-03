package ports

import (
	"fmt"
	"strings"
	"time"
)

type PortType int

const (
	PortTypeUser PortType = iota
	PortTypeDocker
)

func (pt PortType) String() string {
	switch pt {
	case PortTypeUser:
		return "user"
	case PortTypeDocker:
		return "docker"
	default:
		return "unknown"
	}
}

type ListeningPort struct {
	Port      int
	PID       int
	Process   string // short name (e.g. "node")
	Command   string // full cmdline from ps
	ParentCmd string // parent process cmdline (for unwrapping reload supervisors)
	Cwd       string // process working directory
	// CwdGone says that directory is not there any more: the process is still
	// holding its port while the project behind it is deleted. Where a path is
	// (InTrash) and whether it exists are two different facts, and this is the
	// second one.
	CwdGone     bool
	User        string
	BindAddress string
	// BindAddresses lists every address this one listener holds, set only when
	// there is more than one: a process that binds both address families shows
	// up as two sockets (`0.0.0.0:3000` and `[::]:3000`, or `127.0.0.1:6379`
	// and `[::1]:6379`) and they are one service on one port. BindAddress stays
	// the primary — the one a reader acts on — and this is here so nothing is
	// lost by folding them.
	BindAddresses []string
	IPVersion     string // "IPv4" / "IPv6"
	Type          PortType

	// Host is the machine this row was seen on: empty or "localhost" for a
	// direct scan, and the registered name for a row that reached the CLI
	// through the daemon from a remote host. It is what puts a HOST column on
	// `oberth list --host "*"`.
	Host string

	// Display is a display name that was already resolved elsewhere. The
	// daemon sets it when it converts a published state.Port back into a
	// scanner row: the wire shape does not carry the parent cmdline
	// DisplayName would need to recompute the same answer. Empty on a direct
	// scan, where DisplayName derives the name itself.
	Display string

	// Tagged-run attribution: filled by the attribution layer (internal/groups)
	// when a listener's ancestor is a pid the run registry knows. The collection
	// layer reports process facts only; who owns them is decided with the run
	// registry in hand.
	Tag        string // the run's service name (a `option-berth run --tag` label)
	RunGroup   string // the run's group, empty for a legacy `option-berth run --tag`
	RunID      string // caller-supplied (or generated) stable run id
	RunRootPID int    // pid of the `option-berth run` ancestor that owns Tag/RunID

	// Contract fields (cross-spec contract §5). Populated during Enrich.
	PPID        int    // parent pid, from the ps -A table Enrich already builds
	Name        string // user rename; always "" until slice F6 adds the store
	ProjectRoot string // nearest ancestor of Cwd containing a .git entry
	Group       string // inferred group name (compose project or project-root base name)
	GroupSource string // "auto" | "file" | "manual" | "start"
	StartedAt   string // RFC3339, parsed from the process table's lstart

	// Process stats
	CPUPercent  float64 // CPU usage percentage
	MemoryRSS   int64   // resident set size in bytes
	ThreadCount int     // number of threads
	StartTime   string  // process start time (raw from ps)
	Uptime      string  // human-readable uptime
	State       string  // process state (running, sleeping, etc.)
	Connections int     // number of established connections on this port

	// Health check fields
	HealthStatus     string
	HealthCode       int
	HealthLatency    time.Duration
	HealthObservedAt string

	// Docker fields (empty if not Docker)
	DockerContainer      string
	DockerImage          string
	DockerComposeService string
	DockerComposeProject string
	DockerContainerPort  int
	// DockerComposeWorkingDir is the com.docker.compose.project.working_dir
	// label: where `docker compose` was invoked. Used by the group resolver to
	// merge a Compose project into the git checkout that owns it.
	DockerComposeWorkingDir string
}

// PortKey returns a unique identifier for this listening socket (port + bind address).
func (lp *ListeningPort) PortKey() string {
	return fmt.Sprintf("%d:%s", lp.Port, lp.BindAddress)
}

// URL returns the HTTP URL for this port using its bind address.
// For wildcard binds (0.0.0.0 on IPv4, :: on IPv6), localhost is used; a
// literal IPv6 address is bracketed so the URL parses.
func (lp *ListeningPort) URL() string {
	return PortURL(lp.BindAddress, lp.Port)
}

// PortURL builds the browsable URL for a bind address and port. It is shared
// with the health prober so both agree on what a wildcard means.
func PortURL(bind string, port int) string {
	host := HostForBind(bind)
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

// HostForBind maps a bind address to the host a client should dial: a wildcard
// becomes localhost, anything else is itself.
func HostForBind(bind string) string {
	switch bind {
	case "", "0.0.0.0", "*", "::", "[::]":
		return "localhost"
	}
	return strings.TrimSuffix(strings.TrimPrefix(bind, "["), "]")
}

// FindAllByPort returns all listening entries matching the given port number.
func FindAllByPort(port int, all []ListeningPort) []ListeningPort {
	var matches []ListeningPort
	for _, p := range all {
		if p.Port == port {
			matches = append(matches, p)
		}
	}
	return matches
}

// DisplayName returns the best human-readable name for the process.
// Priority: compose service > container name > `oberth start` run name >
// resolved cmdline (with parent + cwd context) > process name.
//
// All signal collection (cmdline, parent cmdline, cwd, service unit) is done
// during Enrich. This method is a pure view over those fields and is safe to
// call from anywhere without I/O.
func (lp *ListeningPort) DisplayName() string {
	if lp.Display != "" {
		return lp.Display
	}
	if lp.DockerComposeService != "" {
		return lp.DockerComposeService
	}
	if lp.DockerContainer != "" {
		return lp.DockerContainer
	}
	// A `oberth start` run already named this service (`--name api`, or the
	// name inferred from its command). Nothing the process table can say beats
	// the name its owner gave it; a user rename still wins, one level up.
	if lp.Tag != "" {
		return lp.Tag
	}
	if name := resolveProcessName(lp.Command, lp.ParentCmd, lp.Cwd); name != "" {
		return name
	}
	return lp.Process
}
