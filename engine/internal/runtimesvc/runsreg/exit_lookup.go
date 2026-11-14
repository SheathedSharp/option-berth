package runsreg

import "time"

// ExitForRun looks up the exact spawned generation, not the latest use of a
// numeric PID. An unrelated newer exit must not hide this run's own outcome.
// It returns a copied record under the registry lock and performs no I/O.
func (r *Registry) ExitForRun(id string, pid int, startedAt time.Time) (Exit, bool) {
	if id == "" || pid <= 0 || startedAt.IsZero() {
		return Exit{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.exits) - 1; i >= 0; i-- {
		e := r.exits[i]
		if e.ID == id && e.PID == pid && e.StartedAt.Equal(startedAt) && !e.ExitedAt.Before(startedAt) {
			return e, true
		}
	}
	return Exit{}, false
}
