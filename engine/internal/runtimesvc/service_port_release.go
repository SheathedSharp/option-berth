package runtimesvc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/claims"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

type groupRunReleaseGuard interface {
	WithNoGroupRuns(context.Context, string, string, func() (int, error)) (int, error)
}

// New registries can queue only the final retry within its existing budget;
// older/extension registries retain the conservative nonblocking fallback.
type groupRunReleaseWaitGuard interface {
	WithNoGroupRunsWait(context.Context, string, string, func() (int, error)) (int, error)
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
	project, worktree := claims.Identity(cfg.Dir)
	for _, svc := range cfg.Services {
		if svc.PortAuto {
			keys[(claims.Address{Project: project, Worktree: worktree, Service: svc.Name}).Key()] = true
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	if rt.Store == nil {
		return nil, errNoStore()
	}
	if !claimsMu.TryLock() {
		return nil, busy("claims are changing")
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

// captureWorktreePortRelease is the same capture for a checkout whose manifest
// is gone. It selects every reservation recorded for the identity's claim key,
// not just the services a manifest would name, because there is no manifest
// left to enumerate.
func captureWorktreePortRelease(ctx context.Context, rt *Runtime, project, worktree string) (*servicePortRelease, error) {
	if project == "" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if rt.Store == nil {
		return nil, errNoStore()
	}
	if !claimsMu.TryLock() {
		return nil, busy("claims are changing")
	}
	defer claimsMu.Unlock()
	var observed store.ClaimObservation
	if err := retryingBusy(ctx, func() error {
		var err error
		observed, err = rt.Store.Claims().ObserveWorktreeContext(ctx, project, worktree)
		return err
	}); err != nil {
		return nil, err
	}
	if len(observed.Rows) == 0 {
		return nil, nil
	}
	return &servicePortRelease{store: rt.Store, configPath: "", observed: observed}, nil
}

func (p *servicePortRelease) release(ctx context.Context, rt *Runtime, group string, after state.Snapshot, scanErr error, results []state.KillResult) (int, error) {
	return p.releaseWithWait(ctx, rt, group, after, scanErr, results, false)
}

func (p *servicePortRelease) releaseWithWait(ctx context.Context, rt *Runtime, group string, after state.Snapshot, scanErr error, results []state.KillResult, wait bool) (int, error) {
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
		return 0, busy("claims are changing")
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
	commit := guard.WithNoGroupRuns
	if wait {
		if queued, ok := rt.runs.(groupRunReleaseWaitGuard); ok {
			commit = queued.WithNoGroupRunsWait
		}
	}
	var released int
	err := retryingBusy(ctx, func() error {
		var err error
		released, err = commit(ctx, group, p.configPath, func() (int, error) {
			return p.store.Claims().DeleteObservedContext(ctx, p.observed)
		})
		return err
	})
	return released, err
}

// busyRetryInterval is how long one retry waits. The lock it is waiting for is
// held for one history insert, so this is a bound on patience, not a poll.
const busyRetryInterval = 20 * time.Millisecond

// busyRetries is how many times a store contention is retried inside the
// caller's deadline.
const busyRetries = 25

// retryingBusy runs work, retrying while the store reports that another writer
// holds the reservations table.
//
// Refusing the first contention made `down` fail outright whenever a scan
// happened to be recording a port event: the release then reported "port
// reservations retained" for a condition that resolves itself in milliseconds.
// The retry is bounded and cancellable, so a genuinely stuck writer still ends
// the call at the caller's deadline rather than holding it open.
func retryingBusy(ctx context.Context, work func() error) error {
	var err error
	for attempt := 0; attempt < busyRetries; attempt++ {
		if err = work(); !errors.Is(err, store.ErrClaimsBusy) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(busyRetryInterval):
		}
	}
	return err
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
		// The code travels: a contention the caller can retry is runtime_service_busy,
		// and everything else here is a real fault. Reporting both as
		// "internal" told an agent to distrust its own retry.
		code := rpc.CodeInternal
		var rpcErr *rpc.Error
		if errors.As(err, &rpcErr) {
			code = rpcErr.Code
		}
		return nil, rpc.NewError(code, "stop attempted; port reservations retained: "+err.Error(),
			"inspect status and runtime service log, then retry down; already sent signals were not undone")
	}
	env.Released = released
	return env, nil
}

// busy is the failure a caller is asked to retry: another operation holds what
// this one needs, and the hold is short. It is a condition, not an engine fault,
// which is the whole difference between an agent retrying and an agent giving
// up on the runtime.service.
func busy(what string) error {
	return rpc.NewError(rpc.CodeBusy, what,
		"retry in a moment; the runtime service is finishing another reservation operation")
}
