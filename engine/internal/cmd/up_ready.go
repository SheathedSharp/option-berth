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
	chunks []rpc.GroupsStartChunk, summary *rpc.GroupsStartEnd, timeout time.Duration,
	timeoutSet bool) ([]string, error) {
	waitStart := time.Now()
	// An explicit --wait-timeout is the ceiling for the whole invocation. It
	// does not replace each service's own readiness budget; it only caps the
	// deadline that budget would otherwise give it.
	var hardDeadline time.Time
	if timeoutSet {
		if timeout <= 0 {
			timeout = groups.DefaultReadyTimeout
		}
		hardDeadline = waitStart.Add(timeout)
	}
	selected := make(map[string]startSelection)
	for _, chunk := range chunks {
		if chunk.Error == "" {
			selected[chunk.Service] = startSelection{
				runID:  chunk.RunID,
				owned:  !chunk.Skipped,
				budget: startChunkReadyTimeout(chunk),
			}
		}
	}
	if len(selected) == 0 {
		return nil, nil
	}

	// ServiceExit.At is second-precision. Comparing it with a nanosecond wait
	// start would read an exit from this same second as older than the wait.
	// Align the timestamp fallback to the second; the run id stays primary.
	fallbackSince := waitStart.UTC().Truncate(time.Second)
	var timedOut []string
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
			group := startTargetGroup(params, cfg, gg)
			for name, selection := range selected {
				var svc *state.Service
				if group != nil {
					svc = serviceIn(*group, name)
				}
				// Anchor each deadline once. A run this invocation started is
				// measured from its own StartedAt, so time already spent
				// starting it counts against its budget. A skipped service,
				// or one whose row has no usable StartedAt, is measured from
				// the moment the wait began.
				if selection.deadline.IsZero() {
					selection = anchorStart(selection, svc, waitStart, hardDeadline)
					selected[name] = selection
				}
				if group == nil || svc == nil {
					continue
				}
				if ready, _ := serviceReady(*group, portsNow, name); ready {
					markStartChunkReady(chunks, name)
					delete(selected, name)
					continue
				}
				// An older daemon may omit run_id on a skipped chunk. Once
				// the service is live, the group row names its run, and
				// that run is what this wait is about.
				if selection.runID == "" && svc.Running && svc.RunID != nil {
					selection.runID = *svc.RunID
					selected[name] = selection
				}
				if selectedExit(svc.LastExit, selection.runID, fallbackSince) {
					markStartChunkFailed(chunks, summary, name, svc.LastExit.Reason,
						fmt.Sprintf("service exited with code %d (%s)", svc.LastExit.Code, svc.LastExit.Reason),
						svc.LastExit.PortHolder)
					delete(selected, name)
				}
			}
		}
		if err != nil {
			// An unreadable snapshot still has to let a service's own budget
			// expire; otherwise a persistent read failure would wait forever.
			for name, selection := range selected {
				if selection.deadline.IsZero() {
					selected[name] = anchorStart(selection, nil, waitStart, hardDeadline)
				}
			}
		}
		if len(selected) == 0 {
			sort.Strings(timedOut)
			return timedOut, nil
		}
		now := time.Now()
		for name, selection := range selected {
			if selection.deadline.IsZero() || now.Before(selection.deadline) {
				continue
			}
			markStartChunkFailed(chunks, summary, name, "ready_timeout",
				startTimeoutMessage(selection, name, timeout), nil)
			// Waiting on an existing run does not authorize stopping it.
			if selection.owned {
				timedOut = append(timedOut, name)
			}
			delete(selected, name)
		}
		if len(selected) == 0 {
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

// startDeadline is when this invocation gives up on one service. A run it
// started is measured from that run's own StartedAt; a skipped service, or one
// whose row carries no usable StartedAt, from the moment the wait began. An
// explicit --wait-timeout caps every deadline at the invocation's ceiling.
func startDeadline(selection startSelection, svc *state.Service, waitStart, hardDeadline time.Time) time.Time {
	return anchorStart(selection, svc, waitStart, hardDeadline).deadline
}

// anchorStart fixes the point a service's budget is measured from and turns
// that budget into a deadline. It records which side set the deadline: an
// explicit --wait-timeout that cut the budget short is reported as such, not
// as a budget that ran out.
func anchorStart(selection startSelection, svc *state.Service, waitStart, hardDeadline time.Time) startSelection {
	since := waitStart
	if selection.owned && svc != nil && svc.StartedAt != nil {
		if at, err := time.Parse(time.RFC3339Nano, *svc.StartedAt); err == nil {
			since = at
		}
	}
	selection.deadline = since.Add(selection.budget)
	selection.capped = false
	if !hardDeadline.IsZero() && selection.deadline.After(hardDeadline) {
		selection.deadline = hardDeadline
		selection.capped = true
	}
	return selection
}

// startTimeoutMessage names what actually expired. A ceiling the caller chose
// is not a readiness budget the manifest declared, and printing one as the
// other sends whoever reads the failure after the wrong number.
func startTimeoutMessage(selection startSelection, name string, timeout time.Duration) string {
	if selection.capped && timeout > 0 {
		return fmt.Sprintf("exceeded the %s --wait-timeout waiting for %s to be ready (ready budget %s)",
			timeout, name, selection.budget)
	}
	return fmt.Sprintf("timed out after %s waiting for %s to be ready (ready_timeout)",
		selection.budget, name)
}

// startChunkReadyTimeout is the readiness budget the daemon published for one
// service. A daemon predating ready_timeout omits it; the client then falls
// back to the same default the manifest uses.
func startChunkReadyTimeout(c rpc.GroupsStartChunk) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(c.ReadyTimeout)); err == nil && d > 0 {
		return d
	}
	return groups.DefaultReadyTimeout
}

// startSelection is what this invocation selected for one service: the run it
// is about, whether this invocation started that run (and may therefore stop
// it on timeout), the readiness budget its manifest gives it, and the deadline
// computed from both that budget and where it is measured from.
type startSelection struct {
	runID    string
	owned    bool
	budget   time.Duration
	deadline time.Time
	// capped says the --wait-timeout ceiling, not the service's budget, set
	// the deadline. The failure message must not call the ceiling a budget.
	capped bool
}

// selectedExit reports whether exit is the death of the run this start
// selected. A run id is authoritative: an exit from another run is stale even
// if its timestamp falls inside this wait. Only when no run id is available
// does the timestamp decide, and then the wait-start second is included.
func selectedExit(exit *state.ServiceExit, runID string, since time.Time) bool {
	if exit == nil {
		return false
	}
	if runID != "" && exit.RunID != "" {
		return exit.RunID == runID
	}
	return exitAfter(exit, since)
}

// exitAfter is the fallback for a legacy record that names no run. It reports
// whether the exit was recorded at or after the wait began. A nil exit is no
// evidence either way; an unparseable timestamp is treated as fresh, because a
// malformed record the daemon did produce is more likely this run's than a
// stale one's. since must be truncated to the timestamp's own precision.
func exitAfter(exit *state.ServiceExit, since time.Time) bool {
	if exit == nil {
		return false
	}
	at, err := time.Parse(time.RFC3339, exit.At)
	if err != nil {
		return true
	}
	return !at.Before(since)
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

// markStartChunkReady records that this invocation waited and confirmed the
// service. A skipped service keeps its state; only readiness is annotated.
func markStartChunkReady(chunks []rpc.GroupsStartChunk, name string) {
	for i := range chunks {
		if chunks[i].Service != name || chunks[i].Error != "" {
			continue
		}
		ready := true
		chunks[i].Ready = &ready
		return
	}
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
		chunks[i].Ready = nil
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
