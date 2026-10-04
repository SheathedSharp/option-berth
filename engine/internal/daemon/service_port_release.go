package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/sheathedsharp/option-berth/internal/claims"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/state"
	"github.com/sheathedsharp/option-berth/internal/store"
)

type groupRunReleaseGuard interface {
	WithNoGroupRuns(context.Context, string, string, func() (int, error)) (int, error)
}

// One operation's observed reservation values, not another runtime registry.
// Keep the original store, keys and manifest path; do not derive replacement
// keys from a config or checkout that changed while stopping the services.
type servicePortRelease struct {
	store      *store.Store
	configPath string
	observed   store.ClaimObservation
}

func captureServicePortRelease(ctx context.Context, rt *Runtime, cfg *groups.Config) (*servicePortRelease, error) {
	if cfg == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keys := make(map[string]bool)
	project, worktree := claims.Identity(cfg.Dir, "", "")
	for _, svc := range cfg.Services {
		if svc.PortAuto {
			keys[claims.ServiceKey(project, worktree, svc.Name)] = true
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	if rt.Store == nil {
		return nil, errNoStore()
	}
	if !claimsMu.TryLock() {
		return nil, errors.New("claims are changing; retry down")
	}
	defer claimsMu.Unlock()
	plan := &servicePortRelease{store: rt.Store, configPath: cfg.Path}
	selected := make([]string, 0, len(keys))
	for key := range keys {
		selected = append(selected, key)
	}
	observed, err := rt.Store.Claims().ObserveContext(ctx, selected...)
	if err != nil {
		return nil, err
	}
	plan.observed = observed
	return plan, nil
}

func (p *servicePortRelease) release(ctx context.Context, rt *Runtime, group string, after state.Snapshot, scanErr error, results []state.KillResult) (int, error) {
	if p == nil || len(p.observed.Rows) == 0 {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if scanErr != nil {
		return 0, fmt.Errorf("post-stop observation failed: %w", scanErr)
	}
	// NotFound is deliberately insufficient, even though killEnvelope accepts it.
	// A recycled PID or partial tree stop is not authority to return a reservation.
	for _, result := range results {
		if !result.OK {
			return 0, errors.New("not every stop target was confirmed; reservations retained")
		}
	}
	claimed := make(map[int]bool, len(p.observed.Rows))
	for _, row := range p.observed.Rows {
		claimed[row.Port] = true
	}
	for _, port := range after.Ports {
		// A port still bound by any visible owner is not free, even if attribution
		// changed, the PID is unknown, or the owner is outside the selected group.
		if claimed[port.Port] || inSnapshotGroup(port, group) {
			return 0, errors.New("post-stop observation still contains a listener; reservations retained")
		}
	}
	if rt.Store != p.store {
		return 0, errors.New("reservation store changed while stopping")
	}
	if !claimsMu.TryLock() {
		return 0, errors.New("claims are changing; reservations retained")
	}
	defer claimsMu.Unlock()
	// SetRuns is startup-only in production; hold its read lock through the
	// callback so even an extension cannot swap the authority in this interval.
	rt.runsMu.RLock()
	defer rt.runsMu.RUnlock()
	guard, ok := rt.runs.(groupRunReleaseGuard)
	if !ok {
		return 0, errors.New("run registry cannot confirm safe reservation release")
	}
	return guard.WithNoGroupRuns(ctx, group, p.configPath, func() (int, error) {
		return p.store.Claims().DeleteObservedContext(ctx, p.observed)
	})
}

func finishGroupStop(ctx context.Context, req *Request, group string, plan *servicePortRelease, after state.Snapshot, scanErr error, results []state.KillResult) (any, error) {
	env := killEnvelope(results)
	released, err := plan.releaseAfterContention(ctx, req.Runtime, group, after, scanErr, results)
	if err != nil {
		if req.Runtime.Logger != nil {
			req.Runtime.Logger.Warn("retaining group port reservations after stop", "group", group, "error", err)
		}
		// Preserve the existing wire schema and report failure to CLI clients too;
		// down's text path does not inspect envelope.OK. Signals already sent and
		// registry observations are not undone by this reservation failure.
		return nil, rpc.NewError(rpc.CodeInternal, "stop attempted; port reservations retained: "+err.Error(),
			"inspect status and daemon log, then retry down; already sent signals were not undone")
	}
	env.Released = released
	return env, nil
}
