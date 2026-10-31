package groups

import (
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/sheathedsharp/option-berth/engine/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
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
			exit.PortHolder = result.PortHolder
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
			// A listener's stats describe its process. A portless worker has
			// no listener; the run row below carries its stats instead.
			if listener.Stats != nil {
				stats := *listener.Stats
				row.Stats = &stats
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
			// discriminator.
			//
			// A comparable hash is the whole verdict. Text comparison is the
			// fallback for records that predate it, not an extra check: the
			// runtime command is expanded (`${worktree}`, `${port}`), so it
			// legitimately differs from the manifest's spelling and comparing
			// the two would call every correctly started Compose service stale.
			hashComparable := len(run.SpecHash) == SpecHashLen
			drift := hashComparable && run.SpecHash != *row.ManifestHash
			if !hashComparable {
				drift = (run.Cmd != "" && strings.TrimSpace(run.Cmd) != strings.TrimSpace(svc.Cmd)) ||
					(run.Cwd != "" && filepath.Clean(run.Cwd) != filepath.Clean(cfg.ServiceDir(svc)))
			}
			row.ManifestRuntimeMismatch = drift
		}
		out[i] = row
	}
	return out
}
