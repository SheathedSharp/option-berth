package cmd

// `oberth attn` is the optional model assessment of a runtime anomaly: the
// agent-facing runner, the action preflight, local quality feedback, and the
// person-facing setup verbs. It is one command, not a second binary.
//
// The layer is Handoff, not Facts: this command asks an optional provider about
// a runtime anomaly and hands a choice back to the person. It never starts,
// stops or restarts a service on its own.

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attn"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/runner"
	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/spf13/cobra"
)

// The flags of the whole `attn` family. They are package variables for the same
// reason every other command's are: the tree is built once per process.
var (
	attnJSONFlag      bool
	attnProjectFlag   string
	attnTaskFlag      string
	attnTaskFileFlag  string
	attnAuthorizeFlag []string
	attnIntervalFlag  time.Duration
	attnFollowFlag    bool
	attnDryRunFlag    bool
	attnOberthFlag    string

	attnFeedbackFileFlag   string
	attnArtifactFlag       string
	attnStatusSinceFlag    time.Duration
	attnQualityProjectFlag string

	attnPreflightStatusFlag     string
	attnPreflightFileFlag       string
	attnPreflightActionFlag     string
	attnPreflightServiceFlag    string
	attnPreflightDraftFlag      string
	attnPreflightPermissionFlag string
	attnPreflightApprovedFlag   bool
)

var attnCmd = &cobra.Command{
	Use:     "attn",
	Short:   "Optional model assessment of a runtime anomaly (off unless enabled)",
	GroupID: commandGroupHandoff,
	Long: "Ask an optional model whether a runtime anomaly affects the current task, and\n" +
		"whether the next step needs the person's choice.\n\n" +
		"Off by default. Only `enabled: true` in the owner-only attn.json under\n" +
		"BERTH_HOME, or a one-shot BERTH_ATTN=1 for this process, lets this command\n" +
		"contact a configured endpoint; a key in the environment supplies the secret\n" +
		"but is not consent. `oberth status` publishes fact-only `attn_path` for\n" +
		"an anomaly; its presence does not grant consent or start a session.\n\n" +
		"The command never starts, stops or restarts a service. It reads\n" +
		"`oberth status --json`, validates the published artifact against that exact\n" +
		"revision, sends a redacted summary, and prints one JSON value when the model\n" +
		"cannot answer; the reason is one fixed code (`no_key`, `disabled`,\n" +
		"`unauthorized`, `rate_limited`, `timeout`, `network`, `provider_error`,\n" +
		"`bad_response`, `stale`).",
	Args: cobra.NoArgs,
	RunE: runAttnStatus,
}

var attnAssessCmd = &cobra.Command{
	Use:   "assess",
	Short: "One status read, one anomaly assessment, one handoff",
	Long: "Read `oberth status --json`, verify the published artifact still matches that\n" +
		"revision, ask the configured endpoint, and print the handoff. The authorized\n" +
		"option IDs are the agent's explicit allow-list; artifact options are\n" +
		"candidates, never permission.\n\n" +
		"With --follow, observe runtime changes for this task and emit NDJSON: one\n" +
		"`subscribed` record, then only changed handoffs, and `stopped` on\n" +
		"cancellation. With --dry-run, print the exact redacted request for the\n" +
		"current event without contacting anything.",
	Args: cobra.NoArgs,
	RunE: runAttnAssess,
}

var attnFeedbackCmd = &cobra.Command{
	Use:   "feedback",
	Short: "Record what a person actually decided about one handoff",
	Long: "Append one agent quality-feedback JSON document to the local ledger. This is\n" +
		"the label that makes the ledger answer 'was a person needed'; it never changes\n" +
		"the attention artifact and never grants permission.",
	Args: cobra.NoArgs,
	RunE: runAttnFeedback,
}

var attnPreflightCmd = &cobra.Command{
	Use:   "preflight",
	Short: "Derive and optionally assess a bounded action request",
	Long: "Turn a freshly captured `oberth status --json` snapshot into a bounded preflight\n" +
		"request under BERTH_HOME/attention/preflight. Without consent the request is\n" +
		"still written for the agent to present; with consent only needs_human, task\n" +
		"relevance and the ranking of the fixed options are attached. The command never\n" +
		"executes the action.",
	Args: cobra.NoArgs,
	RunE: runAttnPreflight,
}

func init() {
	attnCmd.PersistentFlags().BoolVar(&attnJSONFlag, "json", false, "Output as JSON")
	attnCmd.PersistentFlags().StringVar(&attnProjectFlag, "project", ".", "Worktree directory to read")
	attnCmd.PersistentFlags().StringVar(&attnOberthFlag, "oberth", "oberth", "oberth executable to read status with")

	attnCmd.PersistentFlags().StringVar(&attnTaskFlag, "task", "", "Task summary (alternative to --task-context)")
	attnCmd.PersistentFlags().StringVar(&attnTaskFileFlag, "task-context", "",
		"JSON file containing task and authorized_options")
	attnCmd.PersistentFlags().StringSliceVar(&attnAuthorizeFlag, "authorize", nil,
		"Authorized option IDs for this task (comma separated); overrides the context file")

	attnAssessCmd.Flags().BoolVar(&attnFollowFlag, "follow", false,
		"Observe task changes and emit only changed handoffs as NDJSON")
	attnAssessCmd.Flags().BoolVar(&attnDryRunFlag, "dry-run", false,
		"Print the redacted request for the current event without sending it")
	attnAssessCmd.Flags().DurationVar(&attnIntervalFlag, "interval", 2*time.Second,
		"Consent check / offline poll interval (with --follow)")

	attnStatusCmd.Flags().DurationVar(&attnStatusSinceFlag, "since", 168*time.Hour,
		"Quality ledger lookback window (0 means all records)")
	attnStatusCmd.Flags().StringVar(&attnQualityProjectFlag, "quality-project", "",
		"Project label or worktree id to filter quality data on")

	attnFeedbackCmd.Flags().StringVar(&attnFeedbackFileFlag, "file", "", "Feedback JSON document to append")
	attnFeedbackCmd.Flags().StringVar(&attnArtifactFlag, "attention", "",
		"Attention artifact the feedback refers to, to check its identity")
	_ = attnFeedbackCmd.MarkFlagRequired("file")

	attnPreflightCmd.Flags().StringVar(&attnPreflightStatusFlag, "status", "",
		"Fresh status --json file to derive or validate the request from")
	attnPreflightCmd.Flags().StringVar(&attnPreflightFileFlag, "preflight", "",
		"Existing preflight JSON to assess")
	attnPreflightCmd.Flags().StringVar(&attnPreflightActionFlag, "action", "",
		"Action: start_service, restart_service, stop_service or adopt_manifest")
	attnPreflightCmd.Flags().StringVar(&attnPreflightServiceFlag, "service", "",
		"Declared service target for start_service or restart_service")
	attnPreflightCmd.Flags().StringVar(&attnPreflightDraftFlag, "draft-group", "",
		"Pending draft group for adopt_manifest")
	attnPreflightCmd.Flags().StringVar(&attnPreflightPermissionFlag, "permission", "",
		"Explicit user-permission context for the proposed action")
	attnPreflightCmd.Flags().BoolVar(&attnPreflightApprovedFlag, "user-approved", false,
		"Record that the user approved the proposed action (does not execute it)")

	attnCmd.AddCommand(attnAssessCmd, attnFeedbackCmd, attnPreflightCmd,
		attnStatusCmd, attnConfigCmd, attnOnCmd, attnOffCmd, attnTestCmd)
	rootCmd.AddCommand(attnCmd)
}

// attnTaskContext loads the agent's explicit authorization. There is no default:
// an unset allow-list is an error, because a candidate option in an artifact is
// not permission to act on it.
func attnTaskContext() (model.TaskContext, error) {
	task, err := attnOptionalTaskContext()
	if err != nil {
		return task, err
	}
	if strings.TrimSpace(task.Task) == "" {
		return task, usageError{errors.New("--task or --task-context is required")}
	}
	if len(task.AuthorizedOptions) == 0 {
		return task, usageError{errors.New("--authorize (or authorized_options) is required")}
	}
	return task, nil
}

// attnOptionalTaskContext applies the task flags without requiring them. The
// action preflight uses this: its request already names the action and target,
// so an agent may omit the development-task summary.
func attnOptionalTaskContext() (model.TaskContext, error) {
	var task model.TaskContext
	var err error
	if strings.TrimSpace(attnTaskFileFlag) != "" {
		task, err = model.ReadTaskContext(attnTaskFileFlag)
		if err != nil {
			return model.TaskContext{}, err
		}
	}
	if strings.TrimSpace(attnTaskFlag) != "" {
		task.Task = attnTaskFlag
	}
	if len(attnAuthorizeFlag) > 0 {
		task.AuthorizedOptions = append([]string(nil), attnAuthorizeFlag...)
	}
	return task, nil
}

// attnResolvedConsent is the one consent resolution every attn verb uses. A
// reason means "do not call the endpoint", never "guess a default".
func attnResolvedConsent() model.Resolved {
	input := model.ResolveInputFromEnvironment()
	input.Settings, input.SettingsPresent, input.SettingsErr = model.LoadLocalConfig()
	return model.Resolve(input)
}

func runAttnAssess(cmd *cobra.Command, _ []string) error {
	if attnFollowFlag && attnDryRunFlag {
		return usageError{errors.New("--follow and --dry-run are mutually exclusive")}
	}
	task, err := attnTaskContext()
	if err != nil {
		runner.AppendRunnerTelemetry(runner.TelemetryHome(), attnProjectFlag, model.TaskContext{}, runner.Output{Status: "error", Reason: "invalid_task_context"}, nil, "runner", 0)
		return err
	}
	if attnDryRunFlag {
		return runAttnDryRun(cmd, task)
	}
	consent := attnResolvedConsent()
	if attnFollowFlag {
		return runAttnFollow(cmd, task, consent)
	}
	// The assessment being off does not mean "no handoff": the runner still reads
	// status and builds the fact-only request, then reports the fixed reason. Only
	// the provider call is skipped, which is what the person refused.
	client, clientErr := consent.Client()
	if clientErr != nil {
		// The client is empty, so the runner treats this as "no key": it never
		// calls out, and the reason it prints is the consent decision.
		client = model.Client{}
	}
	result, err := runner.AssessRecorded(cmd.Context(), attnOberthFlag, attnProjectFlag, runner.TelemetryHome(), client, task, runner.ReadStatus, consent.Reason)
	if err != nil {
		return err
	}
	return writeAttnOutput(cmd.OutOrStdout(), result)
}

// runAttnFollow is the long-lived form of assess. It observes facts and emits
// only changed handoffs, so an agent does not rebuild the request by hand.
func runAttnFollow(cmd *cobra.Command, task model.TaskContext, consent model.Resolved) error {
	if attnIntervalFlag <= 0 {
		return usageError{errors.New("--interval must be positive")}
	}
	// A session on a disabled configuration is not a silent no-op: it reports
	// the fixed reason and stops, so an agent does not wait for events that can
	// never arrive.
	client, clientErr := consent.Client()
	if clientErr != nil {
		result := runner.Output{Status: "unavailable", Reason: consent.ReasonOr(clientErr)}
		runner.AppendRunnerTelemetry(runner.TelemetryHome(), attnProjectFlag, task, result, nil, "subscription", 0)
		return writeAttnOutput(cmd.OutOrStdout(), result)
	}
	subscriptionContext, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	trigger, closeChanges := attentionChanges(subscriptionContext, attnProjectFlag)
	defer closeChanges()
	trigger.Check = runner.CheckConfiguration(attnResolvedConsent, client)
	sessionID := runner.NewSessionID(attnProjectFlag)
	runner.AppendLifecycleTelemetry(runner.TelemetryHome(), attnProjectFlag, "subscribed", sessionID, nil)
	stats, err := runner.SubscribeWithStats(subscriptionContext, attnOberthFlag, attnProjectFlag, attnIntervalFlag,
		client, task, runner.WhileConfigured(attnResolvedConsent, client, runner.ReadStatus), sessionID,
		func(result runner.Output, latency time.Duration) {
			runner.AppendSubscriptionTelemetry(runner.TelemetryHome(), attnProjectFlag, task, result, latency, attnIntervalFlag)
		},
		func(value any) error { return writeAttnValue(cmd.OutOrStdout(), value) }, trigger)
	if err != nil {
		runner.AppendLifecycleTelemetry(runner.TelemetryHome(), attnProjectFlag, "error", sessionID, &stats)
		return err
	}
	runner.AppendLifecycleTelemetry(runner.TelemetryHome(), attnProjectFlag, "stopped", sessionID, &stats)
	return nil
}

// runAttnDryRun prints exactly what the next assessment would send for the
// current event. It is the honesty check, and it is offline by construction.
func runAttnDryRun(cmd *cobra.Command, task model.TaskContext) error {
	status, err := runner.ReadStatus(cmd.Context(), attnOberthFlag, attnProjectFlag)
	if err != nil {
		return err
	}
	if status.AttnPath == "" {
		return attnPrint(map[string]any{"status": "clear"}, func() string {
			return "No current anomaly, so there is nothing to assess."
		})
	}
	artifact, err := attn.ReadFresh(status.AttnPath, time.Time{})
	if err != nil {
		return failHint("stale", "the attention artifact is stale", model.ReasonHint(model.ReasonStale))
	}
	if len(task.AuthorizedOptions) == 0 {
		for _, option := range artifact.Options {
			task.AuthorizedOptions = append(task.AuthorizedOptions, option.ID)
		}
	}
	body, err := model.PreviewRequest(artifact, task)
	if err != nil {
		return usageError{err}
	}
	return attnPrint(map[string]any{"status": "preview", "request": body}, func() string {
		encoded, _ := json.MarshalIndent(body, "", "  ")
		return string(encoded)
	})
}

func runAttnFeedback(cmd *cobra.Command, _ []string) error {
	result, err := runner.Feedback(attnFeedbackFileFlag, attnArtifactFlag, attnProjectFlag)
	if err != nil {
		return err
	}
	return writeAttnValue(cmd.OutOrStdout(), map[string]string{
		"status": "feedback_recorded", "telemetry_path": result.Path, "request_id": result.RecordID,
	})
}

// attnPreflightOutput is the JSON an agent reads back from `attn preflight`.
type attnPreflightOutput struct {
	Status        string           `json:"status"`
	PreflightPath string           `json:"preflight_path,omitempty"`
	TelemetryPath string           `json:"telemetry_path,omitempty"`
	Reason        string           `json:"reason,omitempty"`
	Assessment    *attn.Assessment `json:"assessment,omitempty"`
	TaskRelevant  *float64         `json:"task_relevant,omitempty"`
	Model         string           `json:"model,omitempty"`
	RequestID     string           `json:"request_id,omitempty"`
}

func runAttnPreflight(cmd *cobra.Command, _ []string) error {
	task, err := attnOptionalTaskContext()
	if err != nil {
		return err
	}
	consent := attnResolvedConsent()
	home := paths.Dir()
	var request model.Preflight
	if strings.TrimSpace(attnPreflightFileFlag) != "" {
		request, err = model.ReadPreflight(attnPreflightFileFlag)
		if err != nil {
			return err
		}
		if filepath.Clean(attnPreflightFileFlag) != filepath.Clean(model.PreflightPath(home, request)) {
			return errors.New("preflight path is not the current BERTH_HOME derived request")
		}
		if strings.TrimSpace(attnPreflightStatusFlag) != "" {
			snapshot, readErr := model.ReadStatusSnapshot(attnPreflightStatusFlag, time.Time{})
			if readErr != nil {
				return readErr
			}
			if err := model.ValidatePreflight(request, snapshot, home, time.Time{}); err != nil {
				return err
			}
		}
	} else {
		if strings.TrimSpace(attnPreflightActionFlag) == "" {
			return usageError{errors.New("--action is required with --status")}
		}
		snapshot, readErr := model.ReadStatusSnapshot(attnPreflightStatusFlag, time.Time{})
		if readErr != nil {
			return readErr
		}
		request, err = model.DerivePreflight(snapshot, model.Intent{
			Action: attnPreflightActionFlag, Service: attnPreflightServiceFlag,
			DraftGroup: attnPreflightDraftFlag, UserPermission: attnPreflightPermissionFlag,
			UserApproved: attnPreflightApprovedFlag,
		}, home, time.Time{})
		if err != nil {
			return err
		}
	}
	path, err := model.WritePreflight(home, request)
	if err != nil {
		return err
	}
	// Writing the request is not consent to send it: without consent the agent
	// still gets the bounded preflight document to present directly.
	client, clientErr := consent.Client()
	if clientErr != nil {
		telemetryPath := runner.RecordPreflightAssessment(home, request, task, nil, clientErr)
		return writeAttnValue(cmd.OutOrStdout(), attnPreflightOutput{
			Status: "unavailable", PreflightPath: path, TelemetryPath: telemetryPath,
			RequestID: request.RequestID, Reason: consent.ReasonOr(clientErr),
		})
	}
	artifact := request.AssessmentArtifactForAdapter()
	if strings.TrimSpace(task.Task) == "" {
		// The action intent is the minimum task context when the caller omitted a
		// development summary. The fixed options remain candidates, not permission.
		task.Task = "Evaluate whether this proposed worktree action needs the user's choice"
	}
	task = preflightTaskContext(request, task)
	attempted := false
	result, callErr := client.EvaluateObserved(cmd.Context(), artifact, task, func() { attempted = true })
	var recordedResult *model.Result
	if callErr == nil {
		recordedResult = &result
	}
	telemetryPath := model.TelemetryPath(home)
	defer func() { runner.RecordPreflightAssessment(home, request, task, recordedResult, callErr, attempted) }()
	if callErr != nil {
		if errors.Is(callErr, model.ErrUnavailable) || errors.Is(callErr, model.ErrStale) {
			return writeAttnValue(cmd.OutOrStdout(), attnPreflightOutput{
				Status: "unavailable", PreflightPath: path, TelemetryPath: telemetryPath,
				RequestID: request.RequestID, Reason: model.ReasonFor(callErr),
			})
		}
		return callErr
	}
	if err := request.AttachAssessment(result.Assessment); err != nil {
		callErr = err
		return err
	}
	if _, err := model.WritePreflight(home, request); err != nil {
		callErr = err
		return err
	}
	return writeAttnValue(cmd.OutOrStdout(), attnPreflightOutput{
		Status: "assessed", PreflightPath: path, TelemetryPath: telemetryPath,
		RequestID: request.RequestID, Assessment: &result.Assessment,
		TaskRelevant: &result.TaskRelevant, Model: result.Model,
	})
}

func preflightTaskContext(request model.Preflight, task model.TaskContext) model.TaskContext {
	base := strings.TrimSpace(task.Task)
	target := request.Target.Service
	if target == "" {
		target = request.Target.Project
	}
	if request.Target.DraftGroup != "" {
		target = request.Target.DraftGroup
	}
	suffix := formatPreflightSuffix(request.Intent.Action, target, request.Intent.UserPermission)
	available := 2000 - len([]byte(suffix))
	if len([]byte(base)) > available {
		base = string([]byte(base)[:available])
	}
	task.Task = strings.TrimSpace(base + suffix)
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

func formatPreflightSuffix(action, target, permission string) string {
	suffix := " Proposed fixed action: " + action + "; target: " + target + "; user permission context: " + permission + "."
	if len([]byte(suffix)) > 1800 {
		suffix = string([]byte(suffix)[:1800])
	}
	return suffix
}

// writeAttnOutput prints a runner result. The JSON is the protocol the agent
// reads, so it is written whether or not --json was given; the flag stays for
// symmetry with the rest of the CLI and for the human status view.
func writeAttnOutput(writer io.Writer, result runner.Output) error {
	return writeAttnValue(writer, result)
}

func writeAttnValue(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	return encoder.Encode(value)
}
