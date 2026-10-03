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
		return 0, errors.New("run registry is changing; retain reservations and retry")
	}
	defer r.mirrorMu.Unlock()
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
