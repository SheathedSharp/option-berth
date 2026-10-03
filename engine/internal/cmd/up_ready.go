package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/state"
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
	for _, chunk := range chunks {
		if chunk.Error == "" && !chunk.Skipped {
			pending[chunk.Service] = true
		}
	}
	if len(pending) == 0 {
		return nil, nil
	}

	deadline := time.Now().Add(timeout)
	for {
		pp, gg, err := groupRows(ctx)
		if err == nil {
			portsNow := make([]state.Port, 0, len(pp))
			for _, row := range pp {
				portsNow = append(portsNow, state.FromListening(row))
			}
			if group := startTargetGroup(params, cfg, gg); group != nil {
				for name := range pending {
					if ready, _ := serviceReady(*group, portsNow, name); ready {
						delete(pending, name)
					} else if svc := serviceIn(*group, name); svc != nil && svc.LastExit != nil {
						markStartChunkFailed(chunks, summary, name, svc.LastExit.Reason,
							fmt.Sprintf("service exited with code %d (%s)", svc.LastExit.Code, svc.LastExit.Reason))
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
					fmt.Sprintf("timed out after %s waiting for %s to be ready", timeout, name))
				timedOut = append(timedOut, name)
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
	if svc.PortActual == nil {
		return false, "waiting_for_listener"
	}
	if svc.HealthStatus != nil {
		if svc.HealthStatus.Status == state.HealthOK {
			return true, ""
		}
		return false, "health_" + svc.HealthStatus.Reason
	}
	for _, row := range rows {
		if row.Port != *svc.PortActual {
			continue
		}
		if row.Health == nil {
			return false, "waiting_for_health"
		}
		if row.Health.Status == state.HealthOK {
			return true, ""
		}
		return false, "health_" + row.Health.Reason
	}
	return false, "waiting_for_listener"
}

func markStartChunkFailed(chunks []rpc.GroupsStartChunk, summary *rpc.GroupsStartEnd,
	name, reason, message string) {
	for i := range chunks {
		if chunks[i].Service != name || chunks[i].Error != "" {
			continue
		}
		chunks[i].State = "failed"
		chunks[i].Reason = reason
		chunks[i].Error = message
		chunks[i].Hint = logsHint(name)
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
