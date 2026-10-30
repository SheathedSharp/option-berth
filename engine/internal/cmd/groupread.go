package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/docker"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runs"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// scanPorts and scanProbe are the observation seams. Production uses the real
// collector; tests that exercise a later stage replace them so they do not
// depend on the host's ability to read its own listener table.
var (
	scanPorts = ports.Scan
	scanProbe = ports.ProbeListenerObservation
)

// groupRows joins the worktree's live port evidence with its manifest index.
// Status uses the same source whether the daemon is warm or the caller asks
// for a direct scan.
func groupRows(ctx context.Context) ([]ports.ListeningPort, []state.Group, error) {
	got, groups, _, err := groupRowsObserved(ctx)
	return got, groups, err
}

// groupRowsObserved is groupRows plus the self-probe verdict. A blind
// observation still yields the manifest and run view, with no ports; callers
// that cannot degrade use groupRows and get the same rows, while status can
// say why the port list is unknown.
func groupRowsObserved(ctx context.Context) ([]ports.ListeningPort, []state.Group, *rpc.ObservationStatus, error) {
	var daemonErr error
	if !noDaemonFlag {
		if c, err := dialDaemon(ctx); err == nil {
			defer c.Close()
			var snap state.Snapshot
			if err := c.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snap); err == nil {
				obs := c.Hello().Observation
				if obs == nil || obs.State == string(ports.ObservationObservable) {
					return state.ToListeningAll(snap.Ports), snap.Groups, obs, nil
				}
				// A blind daemon can still serve its last-good snapshot. Treat
				// those ports as unknown, not as current facts; try this process
				// and keep the daemon's reason for the fallback note.
				daemonErr = &cliErr{Code: obs.Code, Msg: "daemon cannot see listeners: " + obs.Detail, Hint: obs.Hint}
			} else {
				daemonErr = cliError(err)
			}
		}
	}
	got, groups, obs, err := scanGroupsObserved(ctx)
	if err != nil {
		if daemonErr == nil {
			return nil, nil, obs, err
		}
		// Both paths failed: keep the local code and hint, but do not lose the
		// daemon's reason. "Daemon is blind" and "this CLI is blind" are two
		// facts a reader needs to choose the next step.
		dcode, dmsg, _ := describe(daemonErr)
		lcode, lmsg, lhint := describe(err)
		return nil, nil, obs, failHint(lcode,
			fmt.Sprintf("%s; daemon also failed: %s: %s", lmsg, dcode, dmsg), lhint)
	}
	if daemonErr != nil {
		noteFallbackBecause(daemonErr)
	}
	return got, groups, obs, nil
}

// scanGroups runs a direct scan, resolves every port's manifest attribution,
// and builds the group collection. Invalid manifests are reported and skipped
// so a broken sibling file does not block status for the current worktree.
func scanGroups() ([]ports.ListeningPort, []state.Group, error) {
	got, groups, _, err := scanGroupsObserved(context.Background())
	return got, groups, err
}

// scanGroupsObserved is the direct path with an observation verdict. When the
// collector cannot produce a trustworthy table, the manifest and run registry
// still describe what is declared and what was started; the ports are unknown.
func scanGroupsObserved(ctx context.Context) ([]ports.ListeningPort, []state.Group, *rpc.ObservationStatus, error) {
	results, scanErr := scanPorts()
	var obs *rpc.ObservationStatus
	if scanErr != nil {
		var oe *ports.ObservationError
		if !errors.As(scanErr, &oe) {
			return nil, nil, nil, scanErr
		}
		probe := scanProbe(ctx)
		obs = observationFromProbe(probe, oe)
		// The probe saw its own listener, so the collector works; an empty
		// first pass may have been transient. One retry settles it without
		// publishing the probe's own socket.
		if oe.Code == ports.CodeObservationEmpty && probe.State == ports.ObservationObservable {
			if retry, err := scanPorts(); err == nil {
				results = retry
			}
		}
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
	return results, groupRows, obs, nil
}

// observationFromProbe turns a failed scan plus the probe verdict into the fact
// a status reader needs. Empty output takes the probe's reason; a format or row
// problem keeps its own code but is still reported as blind to a trustworthy
// table.
func observationFromProbe(probe ports.ProbeResult, scanErr *ports.ObservationError) *rpc.ObservationStatus {
	state, code := probe.State, scanErr.Code
	detail, hint := scanErr.Error(), scanErr.Hint
	switch {
	case scanErr.Code == ports.CodeObservationEmpty:
		code = probe.Code
		// probe.Detail already carries the collector failure; appending it
		// again reads like two separate problems.
		detail = probe.Detail
		if detail == "" {
			detail = scanErr.Error()
		}
		hint = probe.Hint
	case probe.State == ports.ObservationBindDenied:
		state = ports.ObservationBindDenied
		code = probe.Code
		detail = fmt.Sprintf("%s (%s)", probe.Detail, scanErr.Error())
		hint = probe.Hint
	default:
		state = ports.ObservationBlind
		detail = fmt.Sprintf("%s (probe: %s)", scanErr.Error(), probe.Detail)
	}
	return &rpc.ObservationStatus{
		State:     string(state),
		Code:      code,
		Detail:    detail,
		Hint:      hint,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}
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
