package ports

import (
	"fmt"
	"strings"
	"time"
)

// PortType says whether a listener belongs to a native process or a container.
type PortType int

const (
	PortTypeUser PortType = iota
	PortTypeDocker
)

func (kind PortType) String() string {
	switch kind {
	case PortTypeUser:
		return "user"
	case PortTypeDocker:
		return "docker"
	default:
		return "unknown"
	}
}

// ListeningPort is one socket as the collector observed it.
type ListeningPort struct {
	// Socket identity.
	Port      int
	PID       int
	Process   string // short name, such as "node"
	Command   string // full command line from ps
	ParentCmd string // parent command line, for unwrapping supervisors
	Cwd       string // process working directory
	// CwdGone means that directory no longer exists: the process still holds
	// its port while the project behind it was deleted. InTrash says where a
	// path is; this says whether it is still there.
	CwdGone     bool
	User        string
	BindAddress string
	// BindAddresses lists the other addresses one listener holds, present only
	// when there is more than one. A process binding both families appears as
	// two sockets that are one service on one port; BindAddress stays the
	// primary address a reader acts on, and this keeps the fold lossless.
	BindAddresses []string
	IPVersion     string
	Type          PortType

	// Host names the machine the row was seen on: empty or "localhost" for a
	// direct scan, the registered name for a row that came through a daemon.
	Host string

	// Display is a name already resolved elsewhere. The daemon sets it when it
	// turns a published state.Port back into a scanner row, because the wire
	// does not carry the parent command line DisplayName would need. Empty on
	// a direct scan.
	Display string

	// Run attribution, filled by internal/groups when an ancestor is a pid the
	// run registry knows. The collector reports process facts only.
	Tag        string // the run's service name (a `run --tag` label)
	RunGroup   string // the run's group; empty for a legacy `run --tag`
	RunID      string // caller-supplied or generated stable run id
	RunRootPID int    // pid of the `run` ancestor owning Tag/RunID

	// Contract fields, populated during Enrich.
	PPID        int    // parent pid, from the ps table Enrich already builds
	Name        string // user rename; "" until the store supplies one
	ProjectRoot string // nearest ancestor of Cwd holding a .git entry
	Group       string // inferred group (compose project or project-root name)
	GroupSource string // "auto" | "file" | "manual" | "start"
	StartedAt   string // RFC3339, parsed from the process table's lstart

	// Resource sample.
	CPUPercent  float64
	MemoryRSS   int64 // resident set size in bytes
	ThreadCount int
	StartTime   string // raw start time from ps
	Uptime      string // human-readable uptime
	State       string // running, sleeping, …
	Connections int    // established connections on this port

	// Health check.
	HealthStatus     string
	HealthCode       int
	HealthLatency    time.Duration
	HealthObservedAt string

	// Container facts; empty for a native process.
	DockerContainer string
	DockerImage     string

	// Compose placement.
	DockerComposeService string
	DockerComposeProject string
	DockerContainerPort  int
	// DockerComposeWorkingDir is the label recording where `docker compose`
	// ran; groups merges that project into the checkout that owns it.
	DockerComposeWorkingDir string
}

// PortKey is the unique identity of this socket.
func (port *ListeningPort) PortKey() string {
	return fmt.Sprintf("%d:%s", port.Port, port.BindAddress)
}

// URL is the browsable HTTP URL, using localhost for a wildcard bind and
// bracketing a literal IPv6 address so the URL parses.
func (port *ListeningPort) URL() string {
	return PortURL(port.BindAddress, port.Port)
}

// PortURL builds the browsable URL for a bind address and port. The health
// prober shares it, so both agree on what a wildcard means.
func PortURL(address string, number int) string {
	dialHost := HostForBind(address)
	if strings.Contains(dialHost, ":") {
		dialHost = "[" + dialHost + "]"
	}
	return fmt.Sprintf("http://%s:%d", dialHost, number)
}

// HostForBind is the host a client should dial: a wildcard becomes localhost,
// anything else is itself with brackets removed.
func HostForBind(bind string) string {
	switch bind {
	case "", "0.0.0.0", "*", "::", "[::]":
		return "localhost"
	}
	return strings.TrimSuffix(strings.TrimPrefix(bind, "["), "]")
}

// FindAllByPort returns every listener on a port.
func FindAllByPort(port int, all []ListeningPort) []ListeningPort {
	hits := make([]ListeningPort, 0, 1)
	for _, candidate := range all {
		if candidate.Port == port {
			hits = append(hits, candidate)
		}
	}
	return hits
}

// DisplayName is the best human-readable name: compose service, container,
// named run, resolved command line, then process name.
//
// All signals are collected during Enrich; this is a pure view over those
// fields, safe to call anywhere without I/O.
func (port *ListeningPort) DisplayName() string {
	if port.Display != "" {
		return port.Display
	}
	if port.DockerComposeService != "" {
		return port.DockerComposeService
	}
	if port.DockerContainer != "" {
		return port.DockerContainer
	}
	// A named run already gave the service its name; nothing the process table
	// says beats it. A user rename still wins one level up.
	if port.Tag != "" {
		return port.Tag
	}
	if name := resolveProcessName(port.Command, port.ParentCmd, port.Cwd); name != "" {
		return name
	}
	return port.Process
}
