package groups

import (
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/internal/paths"
	"github.com/sheathedsharp/option-berth/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// serviceRowsWithFacts is the contract adapter. Attribution and lifecycle
// decisions belong to servicefacts; manifest metadata and path presentation
// remain here. No registry reads happen while these rows are assembled.
// The resolver spans the whole scan; its indexes are group-scoped, so this
// group's declarations cannot reach another group's listeners.
func serviceRowsWithFacts(cfg *Config, group string, resolver servicefacts.Resolver) []state.Service {
	logDir := paths.Logs()
	groupDir := logDir + string(filepath.Separator) + group + string(filepath.Separator)
	out := make([]state.Service, len(cfg.Services))
	for i, svc := range cfg.Services {
		row := ServiceRow(svc)
		result := resolver.Resolve(servicefacts.Declaration{
			Key: servicefacts.Key{Group: group, Service: svc.Name}, Port: svc.Port, PortAuto: svc.PortAuto,
		})
		row.Running = result.Running
		path := groupDir + svc.Name + ".log"
		row.LogPath = &path
		if result.LastExit != nil {
			exit := *result.LastExit
			row.LastExit = &exit
		}
		if listener := result.Listener; listener != nil {
			port := listener.Port
			row.PortActual = &port
			if listener.PID > 0 {
				pid := listener.PID
				row.PID = &pid
			}
			if listener.Health != nil && listener.Health.Configured {
				health := *listener.Health
				row.HealthStatus = &health
			}
		}
		if run := result.Run; run != nil {
			row.RunID = optional(run.ID)
			row.StartedAt = optional(run.StartedAt)
			row.RuntimeCmd = optional(run.Cmd)
			row.RuntimeCwd = optional(run.Cwd)
			row.RuntimeSpecHash = optional(run.SpecHash)
			if run.PID > 0 {
				pid := run.PID
				row.PID = &pid
			}
			// A runtime spec hash only counts as drift evidence when it was
			// written by this encoding: a run registered before an upgrade
			// carries the previous format, which differs by construction and
			// would flag every surviving service as stale. Length is the
			// discriminator; cmd and cwd still decide those cases.
			hashComparable := len(run.SpecHash) == SpecHashLen
			row.ManifestRuntimeMismatch =
				(hashComparable && run.SpecHash != *row.ManifestHash) ||
					(run.Cmd != "" && strings.TrimSpace(run.Cmd) != strings.TrimSpace(svc.Cmd)) ||
					(run.Cwd != "" && filepath.Clean(run.Cwd) != filepath.Clean(cfg.ServiceDir(svc)))
		}
		out[i] = row
	}
	return out
}
