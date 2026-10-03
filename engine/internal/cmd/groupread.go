package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/docker"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/runs"
	"github.com/sheathedsharp/option-berth/internal/scanner"
	"github.com/sheathedsharp/option-berth/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/internal/state"
	"github.com/sheathedsharp/option-berth/internal/store"
)

// groupRows joins the worktree's live port evidence with its manifest index.
// Status uses the same source whether the daemon is warm or the caller asks
// for a direct scan.
func groupRows(ctx context.Context) ([]ports.ListeningPort, []state.Group, error) {
	if !noDaemonFlag {
		if c, err := dialDaemon(ctx); err == nil {
			defer c.Close()
			var snap state.Snapshot
			if err := c.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snap); err == nil {
				return state.ToListeningAll(snap.Ports), snap.Groups, nil
			}
		}
	}
	return scanGroups()
}

// scanGroups runs a direct scan, resolves every port's manifest attribution,
// and builds the group collection. Invalid manifests are reported and skipped
// so a broken sibling file does not block status for the current worktree.
func scanGroups() ([]ports.ListeningPort, []state.Group, error) {
	results, err := ports.Scan()
	if err != nil {
		return nil, nil, err
	}
	docker.EnrichPorts(results)
	ports.Enrich(results)

	resolved, index := groups.Attribute(results)
	reportInvalidConfigs(index)
	groupRows := groups.GroupsWith(resolved, index, directRunHistory())
	scanner.ProbeConfigured(resolved, groupRows, ports.ProbeHealth, scanner.ConfiguredHealthBudget)
	// Copy the probed verdict back to the scanner rows while retaining direct
	// scan-only evidence (parent command and Compose working directory). The
	// direct path must expose the same health fact as the daemon snapshot;
	// returning the pre-probe rows would silently drop it from status' ports.
	for i := range resolved {
		if i >= len(results) || resolved[i].Health == nil {
			continue
		}
		h := resolved[i].Health
		results[i].HealthStatus = h.Status
		if h.Reason != "" {
			results[i].HealthStatus = h.Reason
		}
		results[i].HealthCode = h.Code
		results[i].HealthLatency = time.Duration(h.LatencyMs) * time.Millisecond
		results[i].HealthObservedAt = h.ObservedAt
	}
	return results, groupRows, nil
}

// directRunRegistry is the no-daemon view of option-berth's own live runs.
// ports.Enrich has already stamped run attribution onto listening rows from
// the same runs.json file; this adapter adds the other half the group builder
// needs: a worker with no port is still running when its recorded PID is alive.
type directRunRegistry struct {
	reg   *runs.Registry
	exits []store.RunExitRow
}

func (r directRunRegistry) Run(p state.Port) (state.Run, bool) {
	return groups.PortRuns{}.Run(p)
}

var _ servicefacts.Provider = directRunRegistry{}

// ServiceFacts projects the already-loaded, pruned registry and durable exit
// history once. Both direct and daemon publication use the same pure reducer.
func (r directRunRegistry) ServiceFacts() servicefacts.Snapshot {
	capacity := len(r.exits)
	if r.reg != nil {
		capacity += len(r.reg.Runs)
	}
	collector := servicefacts.NewCollector(capacity)
	if r.reg != nil {
		for _, entry := range r.reg.Runs {
			collector.AddRun(servicefacts.RunRecord{
				Key: servicefacts.Key{Group: entry.GroupOf(), Service: entry.NameOf()},
				Run: state.ServiceRun{
					ID: entry.ID, PID: entry.PID, StartedAt: entry.StartedAt,
					Cmd: entry.Cmd, Cwd: entry.Cwd, SpecHash: entry.SpecHash,
				},
				PortHint: entry.PortHint,
			})
		}
	}
	for _, exit := range r.exits {
		collector.AddExit(servicefacts.ExitRecord{
			Key: servicefacts.Key{Group: exit.Group, Service: exit.Name},
			Exit: state.ServiceExit{
				Code: exit.Code, Reason: exit.Reason,
				At: exit.ExitedAt.UTC().Format(time.RFC3339), RunID: exit.ID,
			},
		})
	}
	return collector.Snapshot()
}

func directRunHistory() directRunRegistry {
	out := directRunRegistry{reg: runs.Load()}
	if history, err := store.OpenReadOnly(""); err == nil {
		out.exits, _ = history.RunExits(0)
		_ = history.Close()
	}
	return out
}

func reportInvalidConfigs(index *groups.Index) {
	for _, bad := range index.Invalid() {
		fmt.Fprintf(os.Stderr, "warning: ignoring %s\n", bad.Err)
	}
}
