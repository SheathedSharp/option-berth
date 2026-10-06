package runsreg

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

// WithNoGroupRuns serializes a final reservation mutation against Register,
// Unregister, Prune, import and rename using the existing mirror mutation lock.
// It inspects raw records: a compatibility stopping flag is not exit evidence.
// Matching the manifest as well as the group keeps a concurrent rename from
// making this group's still-running services appear absent.
//
// It does not probe or prune. Observation must precede this call. The callback
// may perform the single store transaction but must not call registry mutation,
// scanner, claims-manager or user callbacks. mu is never held during I/O. A
// busy mutation is refused instead of waiting behind an in-flight mirror I/O.
func (r *Registry) WithNoGroupRuns(ctx context.Context, group, configPath string, mutate func() (int, error)) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if group == "" || mutate == nil {
		return 0, errors.New("run release requires a group and a mutation")
	}
	if !r.mirrorMu.TryLock() {
		return 0, reservationBusyError{}
	}
	defer r.mirrorMu.Unlock()
	return r.noGroupRunsLocked(ctx, group, configPath, mutate)
}

// WithNoGroupRunsWait is only the final retry after a fresh stop observation.
// The caller must supply its existing bounded retry context. Waiting holds no
// memory lock, and every raw record is checked again after acquiring ownership.
func (r *Registry) WithNoGroupRunsWait(ctx context.Context, group, configPath string, mutate func() (int, error)) (int, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		return 0, errors.New("reservation handoff requires a deadline")
	}
	if group == "" || mutate == nil {
		return 0, errors.New("run release requires a group and a mutation")
	}
	if err := r.mirrorMu.LockContext(ctx); err != nil {
		return 0, err
	}
	defer r.mirrorMu.Unlock()
	return r.noGroupRunsLocked(ctx, group, configPath, mutate)
}

func (r *Registry) noGroupRunsLocked(ctx context.Context, group, configPath string, mutate func() (int, error)) (int, error) {
	r.mu.Lock()
	present := false
	for _, rec := range r.runs {
		sameConfig := configPath != "" && rec.ConfigPath != "" &&
			strings.EqualFold(filepath.Clean(configPath), filepath.Clean(rec.ConfigPath))
		if strings.EqualFold(rec.Group, group) || sameConfig {
			present = true
			break
		}
	}
	r.mu.Unlock()
	if present {
		return 0, errors.New("group still has registered runs; retain reservations")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return mutate()
}

// Only lock contention is retryable; recorded runs or store failures are not.
// The marker keeps the release caller independent of the concrete registry.
type reservationBusyError struct{}

func (reservationBusyError) Error() string {
	return "run registry is changing; retain reservations and retry"
}
func (reservationBusyError) RetryableReservationRelease() bool { return true }
