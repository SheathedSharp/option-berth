package state

import "github.com/sheathedsharp/option-berth/engine/internal/ports"

// FromListening adapts the scanner model to the published contract model. It
// is the only translation point, so a direct scan and a daemon read emit the
// same rows.
func FromListening(lp ports.ListeningPort) Port {
	p := Port{
		// A row built from a scan is this machine's own; a row that came back
		// through the daemon already carries its optional host label.
		Host:          HostOf(lp.Host),
		Port:          lp.Port,
		BindAddress:   lp.BindAddress,
		BindAddresses: lp.BindAddresses,
		IPVersion:     lp.IPVersion,
		URL:           lp.URL(),
		PID:           lp.PID,
		PPID:          lp.PPID,
		Process:       lp.Process,
		DisplayName:   lp.DisplayName(),
		Name:          strPtr(lp.Name),
		Command:       lp.Command,
		Cwd:           lp.Cwd,
		CwdInTrash:    ports.InTrash(lp.Cwd),
		CwdGone:       lp.CwdGone,
		ProjectRoot:   strPtr(lp.ProjectRoot),
		Group:         strPtr(lp.Group),
		Type:          portType(lp.Type),
		User:          lp.User,
		StartedAt:     strPtr(lp.StartedAt),
	}
	if lp.GroupSource != "" {
		source := GroupSource(lp.GroupSource)
		p.GroupSource = &source
	}
	p.Run = runFromListening(lp)
	p.Stats = statsFromListening(lp)
	if lp.HealthStatus != "" {
		p.Health = healthFromListening(lp)
	}
	p.Docker = dockerFromListening(lp)
	return p
}

// runFromListening is null for anything option-berth did not start. A run
// registered by the older `run --tag` reports its tag as both group and name.
func runFromListening(lp ports.ListeningPort) *Run {
	if lp.RunID == "" && lp.Tag == "" && lp.RunGroup == "" {
		return nil
	}
	return &Run{ID: lp.RunID, Group: lp.RunGroup, Name: lp.Tag, RootPID: lp.RunRootPID}
}

// statsFromListening is null unless something was actually collected.
func statsFromListening(lp ports.ListeningPort) *Stats {
	if lp.CPUPercent == 0 && lp.MemoryRSS == 0 && lp.ThreadCount == 0 &&
		lp.Uptime == "" && lp.State == "" && lp.Connections == 0 {
		return nil
	}
	return &Stats{
		CPUPercent:  lp.CPUPercent,
		MemoryRSS:   lp.MemoryRSS,
		ThreadCount: lp.ThreadCount,

		// The process's current state.
		Uptime:      lp.Uptime,
		State:       lp.State,
		Connections: lp.Connections,
	}
}

func healthFromListening(lp ports.ListeningPort) *Health {
	status, reason := NormalizeHealth(lp.HealthStatus)
	return &Health{
		Status:     status,
		Code:       lp.HealthCode,
		LatencyMs:  lp.HealthLatency.Milliseconds(),
		Reason:     reason,
		ObservedAt: lp.HealthObservedAt,
	}
}

func dockerFromListening(lp ports.ListeningPort) *Docker {
	if lp.DockerContainer == "" && lp.DockerImage == "" && lp.DockerComposeService == "" {
		return nil
	}
	return &Docker{
		Container: lp.DockerContainer,
		Image:     lp.DockerImage,

		// Where Compose placed it.
		ComposeService: lp.DockerComposeService,
		ComposeProject: lp.DockerComposeProject,
		ContainerPort:  lp.DockerContainerPort,
	}
}

// FromListeningAll converts a whole scan result.
func FromListeningAll(pp []ports.ListeningPort) []Port {
	out := make([]Port, 0, len(pp))
	for _, row := range pp {
		out = append(out, FromListening(row))
	}
	return out
}

// portType maps the scanner's integer enum to the contract's string enum.
func portType(t ports.PortType) PortType {
	if t == ports.PortTypeDocker {
		return TypeDocker
	}
	return TypeUser
}

// strPtr keeps absent values null on the wire.
func strPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
