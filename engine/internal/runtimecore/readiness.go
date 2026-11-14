package runtimecore

import (
	"context"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// Service returns the declaration row in one already-scoped group.
func Service(group state.Group, name string) *state.Service {
	for i := range group.Services {
		if group.Services[i].Name == name {
			return &group.Services[i]
		}
	}
	return nil
}

// RequiresObservation keeps an unknown listener/health fact from authorizing readiness.
func RequiresObservation(s *state.Service) bool {
	return s == nil || s.Port != nil || s.PortAuto || (s.Health != nil && *s.Health != "")
}
func Ready(group state.Group, rows []state.Port, name string) (bool, string) {
	svc := Service(group, name)
	if svc == nil || !svc.Running {
		return false, "not_running"
	}
	if svc.Health == nil || *svc.Health == "" {
		if svc.Port != nil || svc.PortAuto {
			if svc.PortActual == nil {
				return false, "waiting_for_listener"
			}
		}
		return true, ""
	}
	check := groups.ParseHealth(*svc.Health)
	// A command check answers for itself: it needs no listener of its own, and
	// its verdict is written straight onto the service row. A service that also
	// declares a port is not ready until that port exists: Compose publishes
	// the host port after the container starts, and a caller reading
	// port_actual right after the wait must not get nil.
	if check.Kind == groups.HealthCommand {
		if (svc.Port != nil || svc.PortAuto) && svc.PortActual == nil {
			return false, "waiting_for_listener"
		}
		return HealthVerdict(svc, nil)
	}
	if svc.PortActual == nil {
		return false, "waiting_for_listener"
	}
	return healthVerdictFor(svc, rows, &group)
}

// healthVerdict reads the service's configured verdict, falling back to the
// port row's when the service row has not been joined yet. rows is nil for a
// command check, whose result has no port row.
func HealthVerdict(svc *state.Service, rows []state.Port) (bool, string) {
	return healthVerdictFor(svc, rows, nil)
}
func healthVerdictFor(svc *state.Service, rows []state.Port, group *state.Group) (bool, string) {
	if svc.HealthStatus != nil {
		if svc.HealthStatus.Status == state.HealthOK {
			return true, ""
		}
		return false, "health_" + svc.HealthStatus.Reason
	}
	if rows != nil && svc.PortActual != nil {
		for _, row := range rows {
			if group != nil && (state.HostOf(row.Host) != state.HostOf(group.Host) || (group.Name != "" && (row.Group == nil || *row.Group != group.Name))) {
				continue
			}
			if svc.RunID != nil && row.Run != nil && row.Run.ID != "" && row.Run.ID != *svc.RunID {
				continue
			}
			if row.Port != *svc.PortActual {
				continue
			}
			if row.Health == nil {
				break
			}
			if row.Health.Status == state.HealthOK {
				return true, ""
			}
			return false, "health_" + row.Health.Reason
		}
	}
	return false, "waiting_for_health"
}

// Replacement and Confirms are shared generation rules, not name matching.
func Replacement(expected string, s *state.Service) (string, bool) {
	if expected == "" || s == nil || !s.Running || s.RunID == nil || *s.RunID == "" || *s.RunID == expected {
		return "", false
	}
	return *s.RunID, true
}
func Confirms(expected string, s *state.Service) bool {
	return expected == "" || (s != nil && s.RunID != nil && *s.RunID == expected)
}

// Restart executes two explicit acts, never an atomic OS transaction. A failed
// or cancelled stop cannot authorize the start half. No automatic rollback,
// retry, widened selector, or successful-start/readiness conflation occurs.
func Restart[S, T any](ctx context.Context, stop func(context.Context) (S, bool, error), start func(context.Context) (T, error)) (stopped S, started T, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	var ok bool
	stopped, ok, err = stop(ctx)
	if err != nil || !ok {
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	started, err = start(ctx)
	return
}
