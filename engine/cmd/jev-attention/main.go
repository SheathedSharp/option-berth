// Command jev-attention is an optional, read-only Jev adapter. It reads a
// fact-only attention artifact and explicit agent context, asks TypeSafe's
// typed endpoint for an assessment, and atomically writes only the validated
// assessment back to that same artifact. It never starts, stops, or inspects a
// service itself.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attention"
	"github.com/sheathedsharp/option-berth/engine/internal/jev"
	"github.com/sheathedsharp/option-berth/engine/internal/jev/runner"
)

type output struct {
	Status        string                `json:"status"`
	AttentionPath string                `json:"attention_path,omitempty"`
	PreflightPath string                `json:"preflight_path,omitempty"`
	TelemetryPath string                `json:"telemetry_path,omitempty"`
	Reason        string                `json:"reason,omitempty"`
	Assessment    *attention.Assessment `json:"assessment,omitempty"`
	TaskRelevant  *float64              `json:"task_relevant,omitempty"`
	Model         string                `json:"model,omitempty"`
	RequestID     string                `json:"request_id,omitempty"`
}

var errMissingOptions = errors.New("task context requires authorized_options or --authorize")

func main() {
	var (
		auto           = flag.Bool("auto", false, "run one agent-side status, anomaly assessment, and HIL handoff cycle")
		subscribe      = flag.Bool("subscribe", false, "keep an agent-side subscription alive and emit HIL events as NDJSON")
		session        = flag.Bool("session", false, "start a task-scoped attention session (alias for --subscribe)")
		watch          = flag.Bool("watch", false, "alias for --subscribe")
		interval       = flag.Duration("interval", 2*time.Second, "poll interval for --subscribe")
		oberth         = flag.String("oberth", "oberth", "oberth executable for --auto/--session")
		project        = flag.String("project", ".", "worktree directory for --auto/--session")
		attentionPath  = flag.String("attention", "", "path from oberth status --json (required)")
		statusPath     = flag.String("status", "", "fresh status --json file for action preflight")
		preflightPath  = flag.String("preflight", "", "existing preflight JSON to assess")
		feedbackPath   = flag.String("feedback", "", "append one agent quality-feedback JSON document")
		qualityReport  = flag.Bool("quality-report", false, "summarize local attention quality telemetry as JSON")
		qualitySince   = flag.Duration("since", 0, "quality-report lookback duration (for example 168h)")
		qualityProject = flag.String("quality-project", "", "quality-report project label or worktree id filter")
		contextPath    = flag.String("task-context", "", "JSON file containing task and authorized_options")
		task           = flag.String("task", "", "task summary (alternative to --task-context)")
		authorized     = flag.String("authorize", "", "comma-separated authorized option IDs (overrides context file)")
		action         = flag.String("action", "", "preflight action: start_service, restart_service, stop_service, or adopt_manifest")
		service        = flag.String("service", "", "declared service target for start_service or restart_service")
		draftGroup     = flag.String("draft-group", "", "pending draft group for adopt_manifest")
		permission     = flag.String("permission", "", "explicit user permission context for action preflight")
		userApproved   = flag.Bool("user-approved", false, "record that the user approved the proposed action (does not execute it)")
		_              = flag.String("endpoint", "", "Jev endpoint (defaults to the resolved provider)")
		_              = flag.String("model", "", "Jev model (defaults to the resolved provider)")
		apiKey         = flag.String("api-key", "", "API key (defaults to jev.json, then the provider's environment variable; pass an empty value to disable)")
		_              = flag.Duration("timeout", 0, "remote request timeout (defaults to jev.json or 10s)")
	)
	flag.Parse()
	if *session {
		*subscribe = true
	}
	// The configured key must never be a flag default, or `--help` would echo a
	// secret. Resolution happens once, here: an explicit --api-key wins verbatim
	// (including an empty value, which disables the key for harnesses), and
	// everything else comes from jev.Resolve so consent has exactly one rule.
	apiKeyProvided := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "api-key" {
			apiKeyProvided = true
		}
	})
	consent := resolveConsent(*apiKey, apiKeyProvided)
	// A user who configured a key and nothing else was relying on the old
	// implicit enable. Say it once per process on stderr instead of failing
	// quietly; the JSON output stays a single value on stdout.
	if consent.MigrationHint {
		fmt.Fprintln(os.Stderr, "jev-attention: Jev now needs explicit consent; environment keys only supply the key. Run `oberth jev enable` once, or set BERTH_JEV=1 for this process.")
	}
	if *qualityReport {
		if *qualitySince < 0 {
			fail("--since must not be negative")
		}
		printQualityReport(*qualitySince, *qualityProject)
		return
	}
	if strings.TrimSpace(*feedbackPath) != "" {
		if *auto || *subscribe || *watch || *statusPath != "" || *preflightPath != "" || *action != "" {
			fail("--feedback cannot be combined with runner or preflight flags")
		}
		printFeedback(*feedbackPath, *attentionPath, *project)
		return
	}
	ctx, contextErr := loadContext(*contextPath, *task, *authorized)
	if *subscribe || *watch {
		if *auto || *attentionPath != "" || *statusPath != "" || *preflightPath != "" || *action != "" {
			fail("--subscribe cannot be combined with --auto, manual artifact, or preflight flags")
		}
		if contextErr != nil {
			fail(contextErr.Error())
		}
		if *interval <= 0 {
			fail("--interval must be positive")
		}
		subscriptionContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		home := runner.TelemetryHome()
		sessionID := runner.NewSessionID(*project)
		runner.AppendLifecycleTelemetry(home, *project, "subscribed", sessionID, nil)
		stats, err := runner.SubscribeWithStats(subscriptionContext, *oberth, *project, *interval, jev.Client{
			Endpoint: consent.Endpoint, Model: consent.Model, APIKey: consent.Key, Timeout: consent.Timeout,
		}, ctx, runner.ReadStatus, sessionID, func(result runner.Output, latency time.Duration) {
			runner.AppendRunnerTelemetry(home, *project, ctx, result, nil, "subscription", latency)
		}, func(value any) error { return writeJSON(os.Stdout, value) })
		if err != nil {
			runner.AppendLifecycleTelemetry(home, *project, "error", sessionID, &stats)
			fail(err.Error())
		}
		runner.AppendLifecycleTelemetry(home, *project, "stopped", sessionID, &stats)
		return
	}
	if *auto {
		if *attentionPath != "" || *statusPath != "" || *preflightPath != "" || *action != "" {
			fail("--auto cannot be combined with manual artifact or preflight flags")
		}
		if contextErr != nil {
			fail(contextErr.Error())
		}
		started := time.Now()
		result, err := runner.Auto(context.Background(), *oberth, *project, jev.Client{
			Endpoint: consent.Endpoint, Model: consent.Model, APIKey: consent.Key, Timeout: consent.Timeout,
		}, ctx, runner.ReadStatus)
		telemetryPath := runner.AppendRunnerTelemetry(runner.TelemetryHome(), *project, ctx, result, err, "runner", time.Since(started))
		if err != nil {
			fail(err.Error())
		}
		// The path is stable even when a diagnostic write is unavailable; expose
		// it so the agent knows where to inspect local quality data.
		result.TelemetryPath = telemetryPath
		printJSON(result)
		return
	}
	if strings.TrimSpace(*statusPath) != "" || strings.TrimSpace(*preflightPath) != "" {
		if contextErr != nil && strings.TrimSpace(*contextPath) != "" {
			fail(contextErr.Error())
		}
		if err := runPreflight(*statusPath, *preflightPath, *action, *service, *draftGroup, *permission, *userApproved, consent, ctx); err != nil {
			fail(err.Error())
		}
		return
	}
	if strings.TrimSpace(*attentionPath) == "" {
		fail("--attention is required unless --auto, --status or --preflight is supplied")
	}
	if contextErr != nil {
		fail(contextErr.Error())
	}
	artifact, err := attention.Read(*attentionPath)
	if err != nil {
		fail(err.Error())
	}
	client, clientErr := consent.Client()
	if clientErr != nil {
		telemetryPath := runner.RecordManualAssessment(runner.TelemetryHome(), *attentionPath, artifact, ctx, nil, clientErr, "manual")
		printJSON(output{Status: "unavailable", AttentionPath: *attentionPath, TelemetryPath: telemetryPath, Reason: consent.ReasonOr(clientErr)})
		return
	}
	result, err := client.Evaluate(context.Background(), artifact, ctx)
	if err != nil {
		if errors.Is(err, jev.ErrUnavailable) || errors.Is(err, jev.ErrStale) {
			telemetryPath := runner.RecordManualAssessment(runner.TelemetryHome(), *attentionPath, artifact, ctx, nil, err, "manual")
			printJSON(output{Status: "unavailable", AttentionPath: *attentionPath, TelemetryPath: telemetryPath, Reason: jev.ReasonFor(err)})
			return
		}
		fail(err.Error())
	}
	if err := jev.Persist(*attentionPath, artifact, result); err != nil {
		if errors.Is(err, jev.ErrStale) {
			printJSON(output{Status: "unavailable", AttentionPath: *attentionPath, Reason: "artifact became stale"})
			return
		}
		fail(err.Error())
	}
	telemetryPath := runner.RecordManualAssessment(runner.TelemetryHome(), *attentionPath, artifact, ctx, &result, nil, "manual")
	printJSON(output{Status: "assessed", AttentionPath: *attentionPath, TelemetryPath: telemetryPath, Assessment: &result.Assessment, TaskRelevant: &result.TaskRelevant, Model: result.Model})
}

func runPreflight(statusPath, preflightPath, action, service, draftGroup, permission string, userApproved bool, consent jev.Resolved, task jev.TaskContext) error {
	home := strings.TrimSpace(os.Getenv("BERTH_HOME"))
	if home == "" {
		return errors.New("preflight requires BERTH_HOME so its derived request has an owned destination")
	}
	var request jev.Preflight
	var err error
	if strings.TrimSpace(preflightPath) != "" {
		request, err = jev.ReadPreflight(preflightPath)
		if err != nil {
			return err
		}
		if filepath.Clean(preflightPath) != filepath.Clean(jev.PreflightPath(home, request)) {
			return errors.New("preflight path is not the current BERTH_HOME derived request")
		}
		if strings.TrimSpace(statusPath) != "" {
			snapshot, readErr := jev.ReadStatusSnapshot(statusPath, time.Time{})
			if readErr != nil {
				return readErr
			}
			if err := jev.ValidatePreflight(request, snapshot, home, time.Time{}); err != nil {
				return err
			}
		}
	} else {
		if strings.TrimSpace(action) == "" {
			return errors.New("--action is required with --status")
		}
		snapshot, readErr := jev.ReadStatusSnapshot(statusPath, time.Time{})
		if readErr != nil {
			return readErr
		}
		request, err = jev.DerivePreflight(snapshot, jev.Intent{
			Action: action, Service: service, DraftGroup: draftGroup,
			UserPermission: permission, UserApproved: userApproved,
		}, home, time.Time{})
		if err != nil {
			return err
		}
	}
	path, err := jev.WritePreflight(home, request)
	if err != nil {
		return err
	}
	// Writing the request is not consent to send it: without consent the agent
	// still gets the bounded preflight document to present directly.
	client, clientErr := consent.Client()
	if clientErr != nil {
		telemetryPath := runner.RecordPreflightAssessment(home, request, task, nil, clientErr)
		printJSON(output{Status: "unavailable", PreflightPath: path, TelemetryPath: telemetryPath, RequestID: request.RequestID, Reason: consent.ReasonOr(clientErr)})
		return nil
	}
	artifact := request.AssessmentArtifactForAdapter()
	if strings.TrimSpace(task.Task) == "" {
		// Preflight's action intent is the minimum task context when the caller
		// intentionally omitted a development summary. The full fixed option
		// subset remains available for Jev to rank; it does not grant permission
		// to execute any of those options.
		task.Task = "Evaluate whether this proposed worktree action needs the user's choice"
	}
	task = preflightTaskContext(request, task)
	result, callErr := client.Evaluate(context.Background(), artifact, task)
	if callErr != nil {
		if errors.Is(callErr, jev.ErrUnavailable) || errors.Is(callErr, jev.ErrStale) {
			telemetryPath := runner.RecordPreflightAssessment(home, request, task, nil, callErr)
			printJSON(output{Status: "unavailable", PreflightPath: path, TelemetryPath: telemetryPath, RequestID: request.RequestID, Reason: jev.ReasonFor(callErr)})
			return nil
		}
		return callErr
	}
	if err := request.AttachAssessment(result.Assessment); err != nil {
		return err
	}
	if _, err := jev.WritePreflight(home, request); err != nil {
		return err
	}
	telemetryPath := runner.RecordPreflightAssessment(home, request, task, &result, nil)
	printJSON(output{Status: "assessed", PreflightPath: path, TelemetryPath: telemetryPath, RequestID: request.RequestID, Assessment: &result.Assessment, TaskRelevant: &result.TaskRelevant, Model: result.Model})
	return nil
}

func preflightTaskContext(request jev.Preflight, task jev.TaskContext) jev.TaskContext {
	base := strings.TrimSpace(task.Task)
	target := request.Target.Service
	if target == "" {
		target = request.Target.Project
	}
	if request.Target.DraftGroup != "" {
		target = request.Target.DraftGroup
	}
	suffix := fmt.Sprintf(" Proposed fixed action: %s; target: %s; user permission context: %s.", request.Intent.Action, target, request.Intent.UserPermission)
	suffixBytes := []byte(suffix)
	if len(suffixBytes) > 1800 {
		suffixBytes = suffixBytes[:1800]
	}
	available := 2000 - len(suffixBytes)
	if len([]byte(base)) > available {
		base = string([]byte(base)[:available])
	}
	task.Task = strings.TrimSpace(base + string(suffixBytes))
	if len(task.AuthorizedOptions) == 0 {
		task.AuthorizedOptions = make([]string, 0, len(request.Options))
		for _, option := range request.Options {
			task.AuthorizedOptions = append(task.AuthorizedOptions, option.ID)
		}
		if len(task.AuthorizedOptions) == 0 && request.Intent.Action != "" {
			task.AuthorizedOptions = []string{request.Intent.Action}
		}
	}
	return task
}

func loadContext(path, task, authorized string) (jev.TaskContext, error) {
	var result jev.TaskContext
	var err error
	if strings.TrimSpace(path) != "" {
		result, err = jev.ReadTaskContext(path)
		if err != nil {
			return jev.TaskContext{}, err
		}
	}
	if strings.TrimSpace(task) != "" {
		result.Task = task
	}
	if strings.TrimSpace(authorized) != "" {
		result.AuthorizedOptions = nil
		for _, option := range strings.Split(authorized, ",") {
			if value := strings.TrimSpace(option); value != "" {
				result.AuthorizedOptions = append(result.AuthorizedOptions, value)
			}
		}
	}
	if strings.TrimSpace(result.Task) == "" {
		return result, errors.New("task context requires --task-context or --task")
	}
	if len(result.AuthorizedOptions) == 0 {
		return result, errMissingOptions
	}
	return result, nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	return encoder.Encode(value)
}

func printJSON(value any) {
	_ = writeJSON(os.Stdout, value)
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}

func printFeedback(feedbackPath, artifactPath, project string) {
	result, err := runner.Feedback(feedbackPath, artifactPath, project)
	if err != nil {
		fail(err.Error())
	}
	printJSON(output{Status: "feedback_recorded", TelemetryPath: result.Path, RequestID: result.RecordID})
}

func printQualityReport(lookback time.Duration, projectFilter string) {
	if lookback < 0 {
		fail("--since must not be negative")
	}
	report, err := runner.QualityReport(lookback, projectFilter)
	if err != nil {
		fail(err.Error())
	}
	printJSON(report)
}
