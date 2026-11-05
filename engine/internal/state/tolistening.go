package state

import (
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// ToListening is the inverse of FromListening: it turns a published row back
// into the scanner model that renderers and OS helpers take. The read handlers
// and every daemon-backed CLI command use it, so a table printed from socket
// rows matches one printed from a direct scan.
//
// The round trip is lossless for everything the CLI renders. Two scanner-only
// fields have no wire form: ParentCmd, whose only job is DisplayName (carried
// as Display instead), and DockerComposeWorkingDir, which the group resolver
// consumes before publication.
func ToListening(p Port) ports.ListeningPort {
	lp := ports.ListeningPort{
		Host:          p.Host,
		Port:          p.Port,
		PID:           p.PID,
		PPID:          p.PPID,
		Process:       p.Process,
		Display:       p.DisplayName,
		Command:       p.Command,
		Cwd:           p.Cwd,
		CwdGone:       p.CwdGone,
		User:          p.User,
		BindAddress:   p.BindAddress,
		BindAddresses: p.BindAddresses,
		IPVersion:     p.IPVersion,
		Type:          listeningType(p.Type),
		Name:          derefStr(p.Name),
		ProjectRoot:   derefStr(p.ProjectRoot),
		Group:         derefStr(p.Group),
		StartedAt:     derefStr(p.StartedAt),
	}
	if p.GroupSource != nil {
		lp.GroupSource = string(*p.GroupSource)
	}
	if p.Run != nil {
		lp.RunID, lp.Tag, lp.RunRootPID, lp.RunGroup =
			p.Run.ID, p.Run.Name, p.Run.RootPID, p.Run.Group
	}
	if p.Stats != nil {
		lp.CPUPercent, lp.MemoryRSS, lp.ThreadCount =
			p.Stats.CPUPercent, p.Stats.MemoryRSS, p.Stats.ThreadCount
		lp.Uptime, lp.State, lp.Connections =
			p.Stats.Uptime, p.Stats.State, p.Stats.Connections
	}
	if p.Health != nil {
		// The CLI's renderers still speak the probe vocabulary, so a known
		// reason is handed back instead of the normalized word.
		status := p.Health.Status
		if reason := p.Health.Reason; reason != "" {
			status = reason
		}
		lp.HealthStatus = status
		lp.HealthCode = p.Health.Code
		lp.HealthLatency = time.Duration(p.Health.LatencyMs) * time.Millisecond
		lp.HealthObservedAt = p.Health.ObservedAt
	}
	if p.Docker != nil {
		lp.DockerContainer, lp.DockerImage =
			p.Docker.Container, p.Docker.Image
		lp.DockerComposeService, lp.DockerComposeProject =
			p.Docker.ComposeService, p.Docker.ComposeProject
		lp.DockerContainerPort = p.Docker.ContainerPort
	}
	return lp
}

// ToListeningAll converts a published collection back to scanner rows.
func ToListeningAll(pp []Port) []ports.ListeningPort {
	out := make([]ports.ListeningPort, 0, len(pp))
	for _, row := range pp {
		out = append(out, ToListening(row))
	}
	return out
}

// listeningType maps the contract's string enum back to the scanner's ints.
func listeningType(t PortType) ports.PortType {
	if t == TypeDocker {
		return ports.PortTypeDocker
	}
	return ports.PortTypeUser
}

func derefStr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
