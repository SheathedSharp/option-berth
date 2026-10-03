// Command jev-attention is an optional, read-only Jev adapter. It reads a
// fact-only attention artifact and explicit agent context, asks TypeSafe's
// typed endpoint for an assessment, and atomically writes only the validated
// assessment back to that same artifact. It never starts, stops, or inspects a
// service itself.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/sheathedsharp/option-berth/internal/attention"
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
		endpoint       = flag.String("endpoint", defaultEndpoint(), "Jev endpoint (environment or ~/.option-berth/jev.json)")
		model          = flag.String("model", defaultModel(), "Jev model")
		apiKey         = flag.String("api-key", "", "API key (defaults to the environment or ~/.option-berth/jev.json; pass an empty value to disable)")
		timeout        = flag.Duration("timeout", defaultTimeout(), "remote request timeout")
	)
	flag.Parse()
	if *session {
		*subscribe = true
	}
	// The configured key must never be a flag default, or `--help` would echo a
	// secret. An explicit --api-key wins verbatim, including an empty value,
	// which disables the key for harnesses.
	apiKeyProvided := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "api-key" {
			apiKeyProvided = true
		}
	})
	*apiKey = effectiveAPIKey(*apiKey, apiKeyProvided)
	if *qualityReport {
		if *qualitySince < 0 {
			fail("--since must not be negative")
		}
		if err := runQualityReport(*qualitySince, *qualityProject); err != nil {
			fail(err.Error())
		}
		return
	}
	if strings.TrimSpace(*feedbackPath) != "" {
		if *auto || *subscribe || *watch || *statusPath != "" || *preflightPath != "" || *action != "" {
			fail("--feedback cannot be combined with runner or preflight flags")
		}
		if err := runFeedback(*feedbackPath, *attentionPath, *project); err != nil {
			fail(err.Error())
		}
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
		home := telemetryHome()
		sessionID := newSessionID(*project)
		appendLifecycleTelemetry(home, *project, "subscribed", sessionID, nil)
		stats, err := runSubscribeWithStats(subscriptionContext, *oberth, *project, *interval, attention.Client{
			Endpoint: *endpoint, Model: *model, APIKey: *apiKey, Timeout: *timeout,
		}, ctx, readRunnerStatus, sessionID, func(result runnerOutput, latency time.Duration) {
			appendRunnerTelemetry(home, *project, ctx, result, nil, "subscription", latency)
		}, func(value any) error { return writeJSON(os.Stdout, value) })
		if err != nil {
			appendLifecycleTelemetry(home, *project, "error", sessionID, &stats)
			fail(err.Error())
		}
		appendLifecycleTelemetry(home, *project, "stopped", sessionID, &stats)
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
		result, err := runAuto(context.Background(), *oberth, *project, attention.Client{
			Endpoint: *endpoint, Model: *model, APIKey: *apiKey, Timeout: *timeout,
		}, ctx, readRunnerStatus)
		telemetryPath := appendRunnerTelemetry(telemetryHome(), *project, ctx, result, err, "runner", time.Since(started))
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
		if err := runPreflight(*statusPath, *preflightPath, *action, *service, *draftGroup, *permission, *userApproved, *endpoint, *model, *apiKey, *timeout, ctx); err != nil {
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
	result, err := (attention.Client{
		Endpoint: *endpoint,
		APIKey:   *apiKey,
		Model:    *model,
		Timeout:  *timeout,
	}).Evaluate(context.Background(), artifact, ctx)
	if err != nil {
		if errors.Is(err, attention.ErrUnavailable) || errors.Is(err, attention.ErrStale) {
			telemetryPath := recordManualAssessment(telemetryHome(), *attentionPath, artifact, ctx, nil, err, "manual")
			printJSON(output{Status: "unavailable", AttentionPath: *attentionPath, TelemetryPath: telemetryPath, Reason: safeReason(err)})
			return
		}
		fail(err.Error())
	}
	if err := attention.Persist(*attentionPath, artifact, result); err != nil {
		if errors.Is(err, attention.ErrStale) {
			printJSON(output{Status: "unavailable", AttentionPath: *attentionPath, Reason: "artifact became stale"})
			return
		}
		fail(err.Error())
	}
	telemetryPath := recordManualAssessment(telemetryHome(), *attentionPath, artifact, ctx, &result, nil, "manual")
	printJSON(output{Status: "assessed", AttentionPath: *attentionPath, TelemetryPath: telemetryPath, Assessment: &result.Assessment, TaskRelevant: &result.TaskRelevant, Model: result.Model})
}

func newSessionID(project string) string {
	root, _ := filepath.Abs(project)
	seed := fmt.Sprintf("%s\x00%d", filepath.Clean(root), time.Now().UnixNano())
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])[:20]
}

func runPreflight(statusPath, preflightPath, action, service, draftGroup, permission string, userApproved bool, endpoint, model, apiKey string, timeout time.Duration, task attention.TaskContext) error {
	home := strings.TrimSpace(os.Getenv("BERTH_HOME"))
	if home == "" {
		return errors.New("preflight requires BERTH_HOME so its derived request has an owned destination")
	}
	var request attention.Preflight
	var err error
	if strings.TrimSpace(preflightPath) != "" {
		request, err = attention.ReadPreflight(preflightPath)
		if err != nil {
			return err
		}
		if filepath.Clean(preflightPath) != filepath.Clean(attention.PreflightPath(home, request)) {
			return errors.New("preflight path is not the current BERTH_HOME derived request")
		}
		if strings.TrimSpace(statusPath) != "" {
			snapshot, readErr := attention.ReadStatusSnapshot(statusPath, time.Time{})
			if readErr != nil {
				return readErr
			}
			if err := attention.ValidatePreflight(request, snapshot, home, time.Time{}); err != nil {
				return err
			}
		}
	} else {
		if strings.TrimSpace(action) == "" {
			return errors.New("--action is required with --status")
		}
		snapshot, readErr := attention.ReadStatusSnapshot(statusPath, time.Time{})
		if readErr != nil {
			return readErr
		}
		request, err = attention.DerivePreflight(snapshot, attention.Intent{
			Action: action, Service: service, DraftGroup: draftGroup,
			UserPermission: permission, UserApproved: userApproved,
		}, home, time.Time{})
		if err != nil {
			return err
		}
	}
	path, err := attention.WritePreflight(home, request)
	if err != nil {
		return err
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
	result, callErr := (attention.Client{Endpoint: endpoint, APIKey: apiKey, Model: model, Timeout: timeout}).Evaluate(context.Background(), artifact, task)
	if callErr != nil {
		if errors.Is(callErr, attention.ErrUnavailable) || errors.Is(callErr, attention.ErrStale) {
			telemetryPath := recordPreflightAssessment(home, request, task, nil, callErr)
			printJSON(output{Status: "unavailable", PreflightPath: path, TelemetryPath: telemetryPath, RequestID: request.RequestID, Reason: safeReason(callErr)})
			return nil
		}
		return callErr
	}
	if err := request.AttachAssessment(result.Assessment); err != nil {
		return err
	}
	if _, err := attention.WritePreflight(home, request); err != nil {
		return err
	}
	telemetryPath := recordPreflightAssessment(home, request, task, &result, nil)
	printJSON(output{Status: "assessed", PreflightPath: path, TelemetryPath: telemetryPath, RequestID: request.RequestID, Assessment: &result.Assessment, TaskRelevant: &result.TaskRelevant, Model: result.Model})
	return nil
}

func preflightTaskContext(request attention.Preflight, task attention.TaskContext) attention.TaskContext {
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

func loadContext(path, task, authorized string) (attention.TaskContext, error) {
	var result attention.TaskContext
	var err error
	if strings.TrimSpace(path) != "" {
		result, err = attention.ReadTaskContext(path)
		if err != nil {
			return attention.TaskContext{}, err
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

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func defaultEndpoint() string {
	if strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")) != "" {
		return envOr("OPENROUTER_ENDPOINT", attention.DefaultOpenRouterEndpoint)
	}
	if strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY")) != "" {
		return envOr("TYPESAFE_ENDPOINT", attention.DefaultJevEndpoint)
	}
	if cfg, present, valid := localJevConfig(); present {
		if !valid || !cfg.Enabled {
			return envOr("TYPESAFE_ENDPOINT", attention.DefaultJevEndpoint)
		}
		if cfg.Provider == "openrouter" {
			return envOr("OPENROUTER_ENDPOINT", firstNonEmpty(cfg.Endpoint, attention.DefaultOpenRouterEndpoint))
		}
		return envOr("TYPESAFE_ENDPOINT", firstNonEmpty(cfg.Endpoint, attention.DefaultJevEndpoint))
	}
	if key, err := attention.ReadLegacyJevKey(); err == nil && key != "" {
		return attention.DefaultOpenRouterEndpoint
	}
	return envOr("TYPESAFE_ENDPOINT", attention.DefaultJevEndpoint)
}

func defaultModel() string {
	if strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")) != "" {
		return envOr("OPENROUTER_MODEL", attention.DefaultOpenRouterModel)
	}
	if strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY")) != "" {
		return envOr("TYPESAFE_MODEL", attention.DefaultJevModel)
	}
	if cfg, present, valid := localJevConfig(); present {
		if !valid || !cfg.Enabled {
			return envOr("TYPESAFE_MODEL", attention.DefaultJevModel)
		}
		if cfg.Provider == "openrouter" {
			return envOr("OPENROUTER_MODEL", firstNonEmpty(cfg.Model, attention.DefaultOpenRouterModel))
		}
		return envOr("TYPESAFE_MODEL", firstNonEmpty(cfg.Model, attention.DefaultJevModel))
	}
	if key, err := attention.ReadLegacyJevKey(); err == nil && key != "" {
		return attention.DefaultOpenRouterModel
	}
	return envOr("TYPESAFE_MODEL", attention.DefaultJevModel)
}

// effectiveAPIKey resolves the key after flag parsing. An explicit --api-key
// wins verbatim, including an empty value, which disables the key for
// harnesses; an unset flag falls back to the environment or the local
// jev.json / decide.key.
func effectiveAPIKey(flagValue string, provided bool) string {
	if provided {
		return flagValue
	}
	return defaultAPIKey()
}

func defaultAPIKey() string {
	if value := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY")); value != "" {
		return value
	}
	if cfg, present, valid := localJevConfig(); present {
		if !valid || !cfg.Enabled {
			return ""
		}
		return cfg.APIKey
	}
	key, err := attention.ReadLegacyJevKey()
	if err != nil {
		return ""
	}
	return key
}

func defaultTimeout() time.Duration {
	if cfg, present, valid := localJevConfig(); present && valid && cfg.Enabled {
		return cfg.Timeout()
	}
	return attention.DefaultJevTimeout
}

// localJevConfig reports a malformed file as present but invalid. This keeps a
// bad or explicitly disabled local setting from falling through to the legacy
// key, while still allowing explicit environment variables to win above it.
func localJevConfig() (attention.LocalJevConfig, bool, bool) {
	cfg, present, err := attention.LoadLocalJevConfig()
	return cfg, present, err == nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func safeReason(err error) string {
	if errors.Is(err, attention.ErrStale) {
		return "artifact became stale"
	}
	if errors.Is(err, attention.ErrUnavailable) {
		return "Jev unavailable"
	}
	return "assessment unavailable"
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
