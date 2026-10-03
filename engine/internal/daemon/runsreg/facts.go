package runsreg

import (
	"time"

	"github.com/sheathedsharp/option-berth/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/internal/state"
)

var _ servicefacts.Provider = (*Registry)(nil)

// ServiceFacts reads runs and exits under the same lock. No process probes or
// storage reads occur here: the registry lifecycle remains the fact source.
// Projection and capture copy only selected scalar facts, without intermediate
// run/history arrays. Service reduction happens later, after releasing the lock.
func (r *Registry) ServiceFacts() servicefacts.Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	collector := servicefacts.NewCollector(len(r.runs) + len(r.exits))
	for _, rec := range r.runs {
		collector.AddRun(servicefacts.RunRecord{
			Key: servicefacts.Key{Group: rec.Group, Service: rec.Name},
			Run: state.ServiceRun{
				ID: rec.ID, PID: rec.PID, StartedAt: rec.StartedAt.UTC().Format(time.RFC3339),
				Cmd: rec.Cmd, Cwd: rec.Cwd, SpecHash: rec.SpecHash,
			},
			StartedAt: rec.StartedAt, PortHint: rec.PortHint, Stopping: rec.stopping,
		})
	}
	for i := len(r.exits) - 1; i >= 0; i-- {
		exit := r.exits[i]
		collector.AddExit(servicefacts.ExitRecord{
			Key: servicefacts.Key{Group: exit.Group, Service: exit.Name}, Exit: exit.ServiceExit(),
		})
	}
	return collector.Snapshot()
}
