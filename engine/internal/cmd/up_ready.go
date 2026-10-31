package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// waitForStartReady waits on the same state that status exposes. A service
// with a configured health path is ready only after its listener reports ok;
// every other service is ready once its run is live (or its listener is up).
// This keeps `up --wait` useful to both humans and agents without adding a
// second readiness protocol to the daemon.
func waitForStartReady(ctx context.Context, params rpc.GroupsStartParams, cfg *groups.Config,
	chunks []rpc.GroupsStartChunk, summary *rpc.GroupsStartEnd, timeout time.Duration) ([]string, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	pending := make(map[string]bool)
	owned := make(map[string]bool)
	for _, chunk := range chunks {
		if chunk.Error == "" {
			pending[chunk.Service] = true
			owned[chunk.Service] = !chunk.Skipped
		}
	}
	if len(pending) == 0 {
		return nil, nil
	}

	deadline := time.Now().Add(timeout)
	// A retained last_exit can predate this start: the run `down` stopped a
	// minute ago still says "stopped, exit 143", and reading it as a fresh
	// failure would flunk every `up --wait` that follows a `down`. Only an
	// exit recorded after the wait began counts as this run's death.
	waitStarted := time.Now().UTC()
	for {
		pp, gg, obs, err := groupRowsObserved(ctx)
		if obs != nil && obs.State != string(ports.ObservationObservable) {
			// Waiting the full timeout would end by stopping the run it just
			// started. "Cannot see" is a different outcome from "not ready".
			code := obs.Code
			if code == "" {
				code = ports.CodeObservationBlind
			}
			return nil, failHint(code,
				fmt.Sprintf("cannot confirm readiness: %s", obs.Detail), obs.Hint)
		}
		if err == nil {
			portsNow := make([]state.Port, 0, len(pp))
			for _, row := range pp {
				portsNow = append(portsNow, state.FromListening(row))
			}
			if group := startTargetGroup(params, cfg, gg); group != nil {
				for name := range pending {
					if ready, _ := serviceReady(*group, portsNow, name); ready {
						delete(pending, name)
					} else if svc := serviceIn(*group, name); svc != nil &&
						exitAfter(svc.LastExit, waitStarted) {
						markStartChunkFailed(chunks, summary, name, svc.LastExit.Reason,
							fmt.Sprintf("service exited with code %d (%s)", svc.LastExit.Code, svc.LastExit.Reason),
							svc.LastExit.PortHolder)
						delete(pending, name)
					}
				}
			}
		}
		if len(pending) == 0 {
			return nil, nil
		}
		if time.Now().After(deadline) {
			timedOut := make([]string, 0, len(pending))
			for name := range pending {
				markStartChunkFailed(chunks, summary, name, "ready_timeout",
					fmt.Sprintf("timed out after %s waiting for %s to be ready", timeout, name), nil)
				// Waiting on an existing run does not authorize stopping it.
				if owned[name] {
					timedOut = append(timedOut, name)
				}
			}
			sort.Strings(timedOut)
			return timedOut, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// exitAfter reports whether an exit record belongs to a run that died after
// the moment a readiness wait began. A nil exit is no evidence either way;
// an unparseable timestamp is treated as fresh, because a malformed record
// the daemon did produce is more likely this run's than a stale one's.
func exitAfter(exit *state.ServiceExit, waitStarted time.Time) bool {
	if exit == nil {
		return false
	}
	at, err := time.Parse(time.RFC3339, exit.At)
	if err != nil {
		return true
	}
	return !at.Before(waitStarted)
}

func startTargetGroup(params rpc.GroupsStartParams, cfg *groups.Config, all []state.Group) *state.Group {
	var configPath string
	if params.ConfigPath != nil {
		configPath = filepath.Clean(*params.ConfigPath)
	} else if cfg != nil {
		configPath = filepath.Clean(cfg.Path)
	}
	name := ""
	if params.Name != nil {
		name = strings.TrimSpace(*params.Name)
	}
	for i := range all {
		if configPath != "" && all[i].ConfigPath != nil && filepath.Clean(*all[i].ConfigPath) == configPath {
			return &all[i]
		}
		if configPath == "" && name != "" && all[i].Name == name {
			return &all[i]
		}
	}
	return nil
}

func serviceIn(group state.Group, name string) *state.Service {
	for i := range group.Services {
		if group.Services[i].Name == name {
			return &group.Services[i]
		}
	}
	return nil
}

func serviceReady(group state.Group, rows []state.Port, name string) (bool, string) {
	svc := serviceIn(group, name)
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
	// its verdict is written straight onto the service row.
	if check.Kind == groups.HealthCommand {
		return healthVerdict(svc, nil)
	}
	if svc.PortActual == nil {
		return false, "waiting_for_listener"
	}
	return healthVerdict(svc, rows)
}

// healthVerdict reads the service's configured verdict, falling back to the
// port row's when the service row has not been joined yet. rows is nil for a
// command check, whose result has no port row.
func healthVerdict(svc *state.Service, rows []state.Port) (bool, string) {
	if svc.HealthStatus != nil {
		if svc.HealthStatus.Status == state.HealthOK {
			return true, ""
		}
		return false, "health_" + svc.HealthStatus.Reason
	}
	if rows != nil && svc.PortActual != nil {
		for _, row := range rows {
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

func markStartChunkFailed(chunks []rpc.GroupsStartChunk, summary *rpc.GroupsStartEnd,
	name, reason, message string, holder *state.PortHolder) {
	for i := range chunks {
		if chunks[i].Service != name || chunks[i].Error != "" {
			continue
		}
		chunks[i].State = "failed"
		chunks[i].Reason = reason
		chunks[i].Error = message
		chunks[i].Hint = logsHint(name)
		chunks[i].PortHolder = holder
		if !containsString(summary.Errors, name) {
			summary.Errors = append(summary.Errors, name)
		}
		return
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
