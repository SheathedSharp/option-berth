package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/internal/attention"
)

// The runner lives in the optional agent-side command. Only the documented
// status read is executed here; all action choices remain with the agent/user.
type runnerStatus struct {
	Scope struct {
		Root string `json:"root"`
	} `json:"scope"`
	AttentionPath     string `json:"attention_path"`
	AttentionRevision string `json:"attention_revision"`
}

type hilDecision struct {
	NeedsHuman bool   `json:"needs_human"`
	Reason     string `json:"reason"`
	NextOption string `json:"next_option,omitempty"`
	Policy     string `json:"policy"`
}

type runnerOption struct {
	ID                     string `json:"id"`
	ReadOnly               bool   `json:"read_only"`
	RequiresExplicitChoice bool   `json:"requires_explicit_choice"`
}

type hilRequest struct {
	Event      attention.Event      `json:"event"`
	Events     []attention.Event    `json:"events,omitempty"`
	Evidence   []attention.Evidence `json:"evidence"`
	Options    []runnerOption       `json:"options"`
	FirstSeen  string               `json:"first_seen,omitempty"`
	LastSeen   string               `json:"last_seen,omitempty"`
	FreshUntil string               `json:"fresh_until,omitempty"`
}

type runnerOutput struct {
	Status            string                `json:"status"`
	Reason            string                `json:"reason,omitempty"`
	AttentionPath     string                `json:"attention_path,omitempty"`
	TelemetryPath     string                `json:"telemetry_path,omitempty"`
	WorktreeID        string                `json:"worktree_id,omitempty"`
	AttentionRevision string                `json:"attention_revision,omitempty"`
	EventID           string                `json:"event_id,omitempty"`
	ContextID         string                `json:"context_id,omitempty"`
	SessionID         string                `json:"session_id,omitempty"`
	FreshUntil        string                `json:"fresh_until,omitempty"`
	Assessment        *attention.Assessment `json:"assessment,omitempty"`
	JevCalled         bool                  `json:"jev_called,omitempty"`
	Cached            bool                  `json:"cached,omitempty"`
	Decision          *hilDecision          `json:"decision,omitempty"`
	Request           *hilRequest           `json:"request,omitempty"`
	// worktreeRoot is used only for the local quality ledger and is deliberately
	// excluded from the agent-facing JSON output.
	worktreeRoot string
}

// subscriptionStats are emitted once when a session stops. Keeping counters
// in the session summary makes a quiet ledger explainable without appending a
// record for every polling tick.
type subscriptionStats struct {
	Polls        int
	EventsSeen   int
	JevCalls     int
	CacheHits    int
	NoEventPolls int
	Unavailable  int
	seen         map[string]bool
}

// Conservative routing thresholds, not claims of calibrated probabilities.
// Even a confident model cannot authorize a side effect. A close Choice
// distribution also needs the user's context.
func decideHIL(a *attention.Assessment) hilDecision {
	d := hilDecision{NeedsHuman: true, Reason: "assessment_unavailable", Policy: "oberth.hil/v1"}
	if a == nil {
		return d
	}
	d.NextOption = a.NextOption
	policy, ok := attention.OptionPolicyFor(a.NextOption)
	if !ok {
		d.Reason = "unknown_option"
		return d
	}
	if policy.RequiresExplicitChoice || !policy.ReadOnly {
		d.Reason = "explicit_choice_required"
		return d
	}
	if a.NeedsHuman == nil || a.Confidence == nil || a.TaskRelevant == nil {
		d.Reason = "incomplete_assessment"
		return d
	}
	if *a.NeedsHuman >= .70 {
		d.Reason = "human_probability_high"
		return d
	}
	if *a.NeedsHuman > .30 || *a.Confidence < .70 {
		d.Reason = "uncertain_assessment"
		return d
	}
	chosen, ok := a.Probabilities[a.NextOption]
	if !ok {
		d.Reason = "incomplete_assessment"
		return d
	}
	for id, probability := range a.Probabilities {
		// Allow a mathematically exact .20 margin despite binary float noise.
		if id != a.NextOption && chosen-probability < .20-1e-9 {
			d.Reason = "close_options"
			return d
		}
	}
	// Ignoring a relevant event is a separate user tradeoff; safe inspection
	// can proceed autonomously when the model is confident it needs no choice.
	if a.NextOption == "continue_without_action" && *a.TaskRelevant > .30 {
		d.Reason = "relevant_event_requires_choice"
		return d
	}
	d.NeedsHuman, d.Reason = false, "confident_read_only_step"
	return d
}

func readRunnerStatus(ctx context.Context, binary, project string) (runnerStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "status", "--json", "--no-mark")
	cmd.Dir = project
	data, err := cmd.Output()
	if err != nil {
		return runnerStatus{}, errors.New("runner: oberth status failed; read oberth status directly for details")
	}
	var status runnerStatus
	// Unmarshal rejects trailing JSON and non-JSON stdout.
	if err := json.Unmarshal(data, &status); err != nil {
		return status, errors.New("runner: invalid oberth status JSON")
	}
	if status.Scope.Root == "" {
		return status, errors.New("runner: status has no worktree scope")
	}
	return status, nil
}

func runnerArtifact(s runnerStatus) (attention.Artifact, error) {
	a, err := attention.ReadFresh(s.AttentionPath, time.Time{})
	if err != nil {
		return a, attention.ErrStale
	}
	if s.AttentionRevision == "" || a.StateRevision != s.AttentionRevision ||
		filepath.Clean(a.WorktreeRoot) != filepath.Clean(s.Scope.Root) ||
		a.EventID == "" || a.RecoveredAt != "" {
		return a, attention.ErrStale
	}
	return a, nil
}

func runAuto(ctx context.Context, binary, project string, client attention.Client, task attention.TaskContext,
	readStatus func(context.Context, string, string) (runnerStatus, error)) (runnerOutput, error) {
	s, err := readStatus(ctx, binary, project)
	if err != nil {
		return runnerOutput{}, err
	}
	if s.AttentionPath == "" {
		return runnerOutput{Status: "clear", WorktreeID: attention.WorktreeID(s.Scope.Root), worktreeRoot: s.Scope.Root}, nil
	}
	a, err := runnerArtifact(s)
	if err != nil {
		return staleRunnerOutput(s), nil
	}
	// Candidate options published by the fact layer are not permission. The
	// agent must supply an explicit allow-list for this task.
	if len(task.AuthorizedOptions) == 0 {
		return runnerOutput{}, errors.New("runner: explicit authorized_options are required")
	}
	// Long-running agents can declare a broad fixed allow-list. Only its
	// intersection with this event's deterministic candidates reaches Jev or
	// the HIL request; candidate publication itself never grants permission.
	authorized, err := attention.FilterAuthorizedOptions(a.Options, task.AuthorizedOptions)
	if err != nil {
		return runnerOutput{}, fmt.Errorf("runner: %w", err)
	}
	task.AuthorizedOptions = authorized
	result, cached := attention.CachedResult(a, task)
	var callErr error
	jevCalled := false
	if !cached {
		jevCalled = strings.TrimSpace(client.APIKey) != ""
		result, callErr = client.Evaluate(ctx, a, task)
	}
	if callErr != nil && !errors.Is(callErr, attention.ErrUnavailable) && !errors.Is(callErr, attention.ErrStale) {
		return runnerOutput{}, callErr
	}
	// A remote call can take seconds. Refresh facts before publishing the HIL
	// output, even on fallback or cache reuse; do not present an old request.
	fresh, err := readStatus(ctx, binary, project)
	if err != nil {
		return runnerOutput{}, err
	}
	if fresh.AttentionPath == "" {
		return runnerOutput{Status: "clear", WorktreeID: attention.WorktreeID(fresh.Scope.Root), worktreeRoot: fresh.Scope.Root}, nil
	}
	latest, err := runnerArtifact(fresh)
	if err != nil || fresh.AttentionPath != s.AttentionPath || latest.EventID != a.EventID || latest.StateRevision != a.StateRevision {
		return staleRunnerOutput(fresh), nil
	}
	if errors.Is(callErr, attention.ErrStale) {
		return staleRunnerOutput(fresh), nil
	}
	if callErr == nil && !cached {
		if err := attention.Persist(s.AttentionPath, a, result); err != nil {
			if errors.Is(err, attention.ErrStale) {
				return staleRunnerOutput(fresh), nil
			}
			return runnerOutput{}, err
		}
	}
	out := runnerOutput{AttentionPath: s.AttentionPath, AttentionRevision: a.StateRevision, EventID: a.EventID, FreshUntil: latest.FreshUntil, Cached: cached, WorktreeID: attention.WorktreeID(fresh.Scope.Root), worktreeRoot: fresh.Scope.Root}
	out.JevCalled = jevCalled
	if callErr == nil {
		out.Assessment = &result.Assessment
		out.ContextID = result.Assessment.ContextID
	}
	d := decideHIL(out.Assessment)
	out.Decision = &d
	if callErr != nil {
		out.Status = "unavailable"
	} else if d.NeedsHuman {
		out.Status = "human_required"
	} else {
		out.Status = "continue"
	}
	request := &hilRequest{
		Event: a.Event, Events: a.Events, Evidence: a.Evidence,
		FirstSeen: a.FirstSeen, LastSeen: a.LastSeen, FreshUntil: a.FreshUntil,
	}
	for _, option := range a.Options {
		for _, allowed := range task.AuthorizedOptions {
			if allowed != option.ID {
				continue
			}
			policy, ok := attention.OptionPolicyFor(option.ID)
			if !ok {
				return runnerOutput{}, errors.New("runner: unknown fixed option")
			}
			request.Options = append(request.Options, runnerOption{ID: option.ID, ReadOnly: policy.ReadOnly, RequiresExplicitChoice: policy.RequiresExplicitChoice})
		}
	}
	out.Request = request
	return out, nil
}

func staleRunnerOutput(status runnerStatus) runnerOutput {
	d := hilDecision{NeedsHuman: true, Reason: "state_changed_refresh_required", Policy: "oberth.hil/v1"}
	return runnerOutput{Status: "stale", WorktreeID: attention.WorktreeID(status.Scope.Root), Decision: &d, worktreeRoot: status.Scope.Root}
}

// runSubscribe keeps the optional adapter outside the daemon while giving an
// agent a durable event stream for the lifetime of its current task. It emits
// only when the status/event/assessment state changes; repeated polls of the
// same revision are deliberately silent.
func runSubscribe(ctx context.Context, binary, project string, interval time.Duration, client attention.Client,
	task attention.TaskContext, readStatus func(context.Context, string, string) (runnerStatus, error),
	emit func(any) error) error {
	return runSubscribeWithObserver(ctx, binary, project, interval, client, task, readStatus, "", nil, emit)
}

// runSubscribeWithObserver keeps the same protocol while allowing the command
// entrypoint to append local quality observations. The observer cannot modify
// the result or execute an option.
func runSubscribeWithObserver(ctx context.Context, binary, project string, interval time.Duration, client attention.Client,
	task attention.TaskContext, readStatus func(context.Context, string, string) (runnerStatus, error),
	sessionID string, observe func(runnerOutput, time.Duration), emit func(any) error) error {
	_, err := runSubscribeWithStats(ctx, binary, project, interval, client, task, readStatus, sessionID, observe, emit)
	return err
}

func runSubscribeWithStats(ctx context.Context, binary, project string, interval time.Duration, client attention.Client,
	task attention.TaskContext, readStatus func(context.Context, string, string) (runnerStatus, error),
	sessionID string, observe func(runnerOutput, time.Duration), emit func(any) error) (subscriptionStats, error) {
	stats := subscriptionStats{seen: make(map[string]bool)}
	if interval <= 0 {
		return stats, errors.New("runner: subscription interval must be positive")
	}
	subscribed := map[string]any{"status": "subscribed", "interval_ms": interval.Milliseconds()}
	if sessionID != "" {
		subscribed["session_id"] = sessionID
	}
	if err := emit(subscribed); err != nil {
		return stats, err
	}
	lastKey := ""
	for {
		if err := ctx.Err(); err != nil {
			_ = emit(subscriptionStopped(sessionID))
			return stats, nil
		}
		stats.Polls++
		started := time.Now()
		result, err := runAuto(ctx, binary, project, client, task, readStatus)
		latency := time.Since(started)
		if err != nil {
			result = runnerOutput{Status: "error", Reason: subscriptionReason(err)}
		}
		result.SessionID = sessionID
		if result.Status == "clear" && result.EventID == "" {
			stats.NoEventPolls++
		}
		if result.EventID != "" {
			caseKey := result.EventID + "\x00" + result.AttentionRevision
			if !stats.seen[caseKey] {
				stats.seen[caseKey] = true
				stats.EventsSeen++
			}
		}
		if result.Cached {
			stats.CacheHits++
		}
		if result.JevCalled {
			stats.JevCalls++
		}
		if result.Status == "unavailable" {
			stats.Unavailable++
		}
		key := subscriptionKey(result)
		if key != lastKey {
			if observe != nil {
				observe(result, latency)
			}
			if err := emit(result); err != nil {
				return stats, err
			}
			lastKey = key
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = emit(subscriptionStopped(sessionID))
			return stats, nil
		case <-timer.C:
		}
	}
}

func subscriptionStopped(sessionID string) map[string]string {
	result := map[string]string{"status": "stopped", "reason": "subscription_cancelled"}
	if sessionID != "" {
		result["session_id"] = sessionID
	}
	return result
}

func subscriptionKey(result runnerOutput) string {
	assessment, _ := json.Marshal(result.Assessment)
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", result.Status, result.EventID,
		result.AttentionRevision, result.DecisionReason(), string(assessment))
}

func (result runnerOutput) DecisionReason() string {
	if result.Decision == nil {
		return result.Reason
	}
	return result.Decision.Reason
}

func subscriptionReason(err error) string {
	if errors.Is(err, attention.ErrStale) {
		return "state_changed_refresh_required"
	}
	if errors.Is(err, attention.ErrUnavailable) {
		return "jev_unavailable"
	}
	return "runner_cycle_failed"
}
