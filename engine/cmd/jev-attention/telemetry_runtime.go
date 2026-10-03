package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/internal/attention"
	"github.com/sheathedsharp/option-berth/internal/paths"
)

// telemetryHome follows the same BERTH_HOME resolution as the engine. The
// quality ledger is local and opt-out by isolation (BERTH_HOME), never by a
// provider or network setting.
func telemetryHome() string { return paths.Dir() }

// appendRunnerTelemetry is best effort by design. A diagnostics failure must
// never turn a safe HIL result into a runtime failure or execute an action.
func appendRunnerTelemetry(home, project string, task attention.TaskContext, result runnerOutput, runErr error, source string, latency time.Duration) string {
	root := result.worktreeRoot
	if strings.TrimSpace(root) == "" {
		root, _ = filepath.Abs(project)
	}
	record := attention.TelemetryRecord{
		Kind: "assessment", Source: source, Project: attention.ProjectLabel(root),
		WorktreeID: attention.WorktreeID(root), Status: result.Status, Reason: result.Reason,
		EventID: result.EventID, StateRevision: result.AttentionRevision,
		SessionID:  result.SessionID,
		FreshUntil: result.FreshUntil, Cached: result.Cached,
		LatencyMS: latency.Milliseconds(), AuthorizedOptions: append([]string(nil), task.AuthorizedOptions...),
	}
	if result.JevCalled {
		record.JevCalls = 1
	}
	if result.WorktreeID != "" {
		record.WorktreeID = result.WorktreeID
	}
	if runErr != nil {
		record.Status = "error"
		record.Reason = telemetryErrorReason(runErr)
	}
	if result.Decision != nil {
		record.Decision = &attention.TelemetryDecision{
			NeedsHuman: result.Decision.NeedsHuman, Reason: result.Decision.Reason, Policy: result.Decision.Policy,
		}
	}
	if result.Assessment != nil {
		assessment := *result.Assessment
		record.Assessment = &assessment
		record.ContextID = assessment.ContextID
	}
	if result.Request != nil {
		record.EventKind = string(result.Request.Event.Kind)
		record.Service = result.Request.Event.Service
		record.FirstSeen = result.Request.FirstSeen
		record.FreshUntil = firstNonEmptyTelemetry(result.FreshUntil, result.Request.FreshUntil)
		record.CandidateOptions = make([]string, 0, len(result.Request.Options))
		for _, option := range result.Request.Options {
			record.CandidateOptions = append(record.CandidateOptions, option.ID)
		}
	}
	if record.Status == "" {
		record.Status = "error"
		record.Reason = "runner_error"
	}
	path, err := attention.AppendTelemetry(home, record)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jev-attention: quality telemetry unavailable: %v\n", err)
		return attention.TelemetryPath(home)
	}
	return path
}

func appendLifecycleTelemetry(home, project, status, sessionID string, stats *subscriptionStats) {
	root, _ := filepath.Abs(project)
	record := attention.TelemetryRecord{
		Kind: "lifecycle", Source: "subscription", Project: attention.ProjectLabel(root),
		WorktreeID: attention.WorktreeID(root), SessionID: sessionID, Status: status,
	}
	if stats != nil {
		record.Polls = stats.Polls
		record.EventsSeen = stats.EventsSeen
		record.JevCalls = stats.JevCalls
		record.CacheHits = stats.CacheHits
		record.NoEventPolls = stats.NoEventPolls
		record.Unavailable = stats.Unavailable
	}
	_, _ = attention.AppendTelemetry(home, record)
}

func recordManualAssessment(home, artifactPath string, artifact attention.Artifact, task attention.TaskContext, result *attention.Result, callErr error, source string) string {
	record := attention.TelemetryRecord{
		Kind: "assessment", Source: source, Project: attention.ProjectLabel(artifact.WorktreeRoot),
		WorktreeID: attention.WorktreeID(artifact.WorktreeRoot), EventID: artifact.EventID,
		StateRevision: artifact.StateRevision, EventKind: string(artifact.Event.Kind),
		Service: artifact.Event.Service, FirstSeen: artifact.FirstSeen, FreshUntil: artifact.FreshUntil,
		CandidateOptions: make([]string, 0, len(artifact.Options)), AuthorizedOptions: append([]string(nil), task.AuthorizedOptions...),
	}
	for _, option := range artifact.Options {
		record.CandidateOptions = append(record.CandidateOptions, option.ID)
	}
	if result != nil {
		assessment := result.Assessment
		record.Assessment = &assessment
		record.ContextID = assessment.ContextID
		decision := decideHIL(&assessment)
		record.Decision = &attention.TelemetryDecision{NeedsHuman: decision.NeedsHuman, Reason: decision.Reason, Policy: decision.Policy}
		record.Status = "assessed"
	} else {
		record.Status = "unavailable"
		record.Reason = telemetryErrorReason(callErr)
	}
	if _, err := attention.AppendTelemetry(home, record); err != nil {
		fmt.Fprintf(os.Stderr, "jev-attention: quality telemetry unavailable: %v\n", err)
	}
	return attention.TelemetryPath(home)
}

func recordPreflightAssessment(home string, request attention.Preflight, task attention.TaskContext, result *attention.Result, callErr error) string {
	record := attention.TelemetryRecord{
		Kind: "assessment", Source: "preflight", Project: attention.ProjectLabel(request.WorktreeRoot),
		WorktreeID: attention.WorktreeID(request.WorktreeRoot), EventID: request.RequestID,
		StateRevision: request.StateRevision, EventKind: "agent_action_intent", Service: request.Target.Service,
		FirstSeen: request.GeneratedAt, FreshUntil: request.FreshUntil,
		CandidateOptions: make([]string, 0, len(request.Options)), AuthorizedOptions: append([]string(nil), task.AuthorizedOptions...),
	}
	for _, option := range request.Options {
		record.CandidateOptions = append(record.CandidateOptions, option.ID)
	}
	if result != nil {
		assessment := result.Assessment
		record.Assessment = &assessment
		record.ContextID = assessment.ContextID
		decision := decideHIL(&assessment)
		record.Decision = &attention.TelemetryDecision{NeedsHuman: decision.NeedsHuman, Reason: decision.Reason, Policy: decision.Policy}
		record.Status = "assessed"
	} else {
		record.Status = "unavailable"
		record.Reason = telemetryErrorReason(callErr)
	}
	if _, err := attention.AppendTelemetry(home, record); err != nil {
		fmt.Fprintf(os.Stderr, "jev-attention: quality telemetry unavailable: %v\n", err)
	}
	return attention.TelemetryPath(home)
}

func telemetryErrorReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, attention.ErrUnavailable) {
		return "jev_unavailable"
	}
	if errors.Is(err, attention.ErrStale) {
		return "state_changed_refresh_required"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "authorized_options") || strings.Contains(message, "task context"):
		return "invalid_task_context"
	case strings.Contains(message, "status"):
		return "status_read_failed"
	default:
		return "runner_error"
	}
}

func firstNonEmptyTelemetry(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func runFeedback(feedbackPath, artifactPath, project string) error {
	var input attention.FeedbackInput
	if err := attention.ReadJSONValueStrict(feedbackPath, &input); err != nil {
		return fmt.Errorf("read feedback: %w", err)
	}
	if strings.TrimSpace(artifactPath) != "" {
		artifact, err := attention.Read(artifactPath)
		if err != nil {
			return fmt.Errorf("read attention artifact: %w", err)
		}
		if artifact.EventID != strings.TrimSpace(input.EventID) || artifact.StateRevision != strings.TrimSpace(input.StateRevision) {
			return errors.New("feedback does not match the supplied attention artifact")
		}
		if input.ContextID != "" && artifact.Assessment != nil && input.ContextID != artifact.Assessment.ContextID {
			return errors.New("feedback context does not match the supplied attention artifact")
		}
		if err := bindFeedbackIdentity(&input, artifact.WorktreeRoot); err != nil {
			return err
		}
	} else if strings.TrimSpace(project) != "" {
		root, err := filepath.Abs(project)
		if err != nil {
			return fmt.Errorf("resolve feedback project: %w", err)
		}
		if err := bindFeedbackIdentity(&input, root); err != nil {
			return err
		}
	}
	record, err := attention.NewFeedbackRecord(input, time.Now().UTC())
	if err != nil {
		return err
	}
	path, err := attention.AppendTelemetry(telemetryHome(), record)
	if err != nil {
		return err
	}
	printJSON(output{Status: "feedback_recorded", TelemetryPath: path, RequestID: record.RecordID})
	return nil
}

// bindFeedbackIdentity makes the command-line project/artifact authoritative.
// A feedback document may omit these fields, but it cannot relabel a case to
// another worktree or project by accident.
func bindFeedbackIdentity(input *attention.FeedbackInput, root string) error {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return errors.New("feedback project root is required")
	}
	expectedWorktree := attention.WorktreeID(root)
	expectedProject := attention.ProjectLabel(root)
	if input.WorktreeID != "" && input.WorktreeID != expectedWorktree {
		return errors.New("feedback worktree_id does not match the project")
	}
	if input.Project != "" && attention.ProjectLabel(input.Project) != expectedProject {
		return errors.New("feedback project does not match the project")
	}
	input.WorktreeID = expectedWorktree
	input.Project = expectedProject
	return nil
}

func runQualityReport(lookback time.Duration, projectFilter string) error {
	home := telemetryHome()
	records, err := attention.ReadTelemetry(home)
	if err != nil {
		return fmt.Errorf("read quality telemetry: %w", err)
	}
	var since time.Time
	if lookback > 0 {
		since = time.Now().UTC().Add(-lookback)
	}
	filter := strings.TrimSpace(projectFilter)
	if filepath.IsAbs(filter) || strings.ContainsRune(filter, filepath.Separator) {
		root, absErr := filepath.Abs(filter)
		if absErr == nil {
			// Accept either the safe project label or its stable worktree hash.
			if filter == "" {
				filter = ""
			} else {
				filter = attention.ProjectLabel(root)
			}
		}
	}
	report := attention.BuildQualityReport(records, since, filter, time.Now().UTC())
	report.Source = attention.TelemetryPath(home)
	printJSON(report)
	return nil
}
