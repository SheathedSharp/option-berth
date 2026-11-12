package cmd

// `oberth attn` carries the attention runner that used to live only in the
// separate `jev-attention` binary. It is the same protocol and the same JSON:
// the standalone command stays as a thin shell over this package during the
// migration, and the scripted acceptance check does not have to change.
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
	"strings"
	"syscall"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/runner"
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
	attnOberthFlag    string

	attnFeedbackFileFlag   string
	attnArtifactFlag       string
	attnRawFlag            bool
	attnQualitySinceFlag   time.Duration
	attnQualityProjectFlag string
)

var attnCmd = &cobra.Command{
	Use:     "attn",
	Short:   "Optional model assessment of a runtime anomaly (off unless enabled)",
	GroupID: commandGroupHandoff,
	Long: "Ask an optional model whether a runtime anomaly affects the current task, and\n" +
		"whether the next step needs the person's choice.\n\n" +
		"Off by default. Only `enabled: true` in the owner-only attn.json under\n" +
		"BERTH_HOME, or a one-shot BERTH_ATTN=1 for this process, lets this command\n" +
		"contact a provider; a key in the environment supplies the secret but is not\n" +
		"consent. `oberth status` only publishes `attn_path` when model is enabled,\n" +
		"so its presence already means the person opted in.\n\n" +
		"The command never starts, stops or restarts a service. It reads\n" +
		"`oberth status --json`, validates the published artifact against that exact\n" +
		"revision, sends a redacted summary, and prints one JSON value when model cannot\n" +
		"answer, the reason is one fixed code (`no_key`, `disabled`, `unauthorized`,\n" +
		"`rate_limited`, `timeout`, `network`, `provider_error`, `bad_response`, `stale`).",
}

var attnAssessCmd = &cobra.Command{
	Use:   "assess",
	Short: "One status read, one anomaly assessment, one handoff",
	Long: "Read `oberth status --json`, verify the published artifact still matches that\n" +
		"revision, ask the provider, and print the handoff. The authorized option IDs\n" +
		"are the agent's explicit allow-list; artifact options are candidates, never\n" +
		"permission. This is the former `jev-attention --auto`.",
	Args: cobra.NoArgs,
	RunE: runAttnAssess,
}

var attnSessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Keep watching for a task and emit only changed handoffs",
	Long: "Poll `oberth status --json` for the lifetime of a task and emit NDJSON: one\n" +
		"`subscribed` record, then only changed event/revision/assessment handoffs, and\n" +
		"`stopped` when the process is cancelled. Repeated polls of unchanged facts are\n" +
		"deliberately silent.",
	Args: cobra.NoArgs,
	RunE: runAttnSession,
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

var attnReportCmd = &cobra.Command{
	Use:   "report",
	Short: "Summarize local attention quality as JSON",
	Long: "Read the local quality ledger and report coverage, assessment availability,\n" +
		"routing reasons, and the labelled precision/recall of the handoff policy. A\n" +
		"metric stays null until there are enough real labels; it is never zero-filled.",
	Args: cobra.NoArgs,
	RunE: runAttnReport,
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
	attnSessionCmd.Flags().DurationVar(&attnIntervalFlag, "interval", 2*time.Second, "Poll interval")

	attnFeedbackCmd.Flags().StringVar(&attnFeedbackFileFlag, "file", "", "Feedback JSON document to append")
	attnFeedbackCmd.Flags().StringVar(&attnArtifactFlag, "attention", "",
		"Attention artifact the feedback refers to, to check its identity")
	_ = attnFeedbackCmd.MarkFlagRequired("file")

	attnReportCmd.Flags().DurationVar(&attnQualitySinceFlag, "since", 0, "Lookback window (for example 168h)")
	attnReportCmd.Flags().StringVar(&attnQualityProjectFlag, "quality-project", "",
		"Project label or worktree id to filter on")

	attnCmd.AddCommand(attnAssessCmd, attnSessionCmd, attnFeedbackCmd, attnReportCmd)
	rootCmd.AddCommand(attnCmd)
}

// attnTaskContext loads the agent's explicit authorization. There is no default:
// an unset allow-list is an error, because a candidate option in an artifact is
// not permission to act on it.
func attnTaskContext() (model.TaskContext, error) {
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
	if strings.TrimSpace(task.Task) == "" {
		return task, usageError{errors.New("--task or --task-context is required")}
	}
	if len(task.AuthorizedOptions) == 0 {
		return task, usageError{errors.New("--authorize (or authorized_options) is required")}
	}
	return task, nil
}

// attnResolvedConsent is the same single resolution the standalone command uses.
// A reason means "do not call the provider", never "guess a default".
func attnResolvedConsent() model.Resolved {
	input := model.ResolveInputFromEnvironment()
	input.Settings, input.SettingsPresent, input.SettingsErr = model.LoadLocalConfig()
	return model.Resolve(input)
}

func runAttnAssess(cmd *cobra.Command, _ []string) error {
	task, err := attnTaskContext()
	if err != nil {
		return err
	}
	consent := attnResolvedConsent()
	started := time.Now()
	// model being off does not mean "no handoff": the runner still reads status
	// and builds the fact-only request, then reports the fixed reason. Only the
	// provider call is skipped, which is what the person refused.
	client, clientErr := consent.Client()
	if clientErr != nil {
		// The client is empty, so the runner treats this as "no key": it never
		// calls out, and the reason it prints is the consent decision.
		client = model.Client{}
	}
	result, err := runner.Auto(cmd.Context(), attnOberthFlag, attnProjectFlag, client, task, runner.ReadStatus)
	if err != nil {
		return err
	}
	if consent.Reason != "" {
		result.Status, result.Reason = "unavailable", consent.Reason
	}
	telemetryPath := runner.AppendRunnerTelemetry(runner.TelemetryHome(), attnProjectFlag, task, result, err, "runner", time.Since(started))
	result.TelemetryPath = telemetryPath
	return writeAttnOutput(cmd.OutOrStdout(), result)
}

func runAttnSession(cmd *cobra.Command, _ []string) error {
	task, err := attnTaskContext()
	if err != nil {
		return err
	}
	if attnIntervalFlag <= 0 {
		return usageError{errors.New("--interval must be positive")}
	}
	consent := attnResolvedConsent()
	// A session on a disabled configuration is not a silent no-op: it reports
	// the fixed reason and stops, so an agent does not wait for events that can
	// never arrive.
	client, clientErr := consent.Client()
	if clientErr != nil {
		return writeAttnOutput(cmd.OutOrStdout(), runner.Output{Status: "unavailable", Reason: consent.ReasonOr(clientErr)})
	}
	subscriptionContext, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sessionID := runner.NewSessionID(attnProjectFlag)
	runner.AppendLifecycleTelemetry(runner.TelemetryHome(), attnProjectFlag, "subscribed", sessionID, nil)
	stats, err := runner.SubscribeWithStats(subscriptionContext, attnOberthFlag, attnProjectFlag, attnIntervalFlag,
		client, task, runner.ReadStatus, sessionID,
		func(result runner.Output, latency time.Duration) {
			runner.AppendRunnerTelemetry(runner.TelemetryHome(), attnProjectFlag, task, result, nil, "subscription", latency)
		},
		func(value any) error { return writeAttnValue(cmd.OutOrStdout(), value) })
	if err != nil {
		runner.AppendLifecycleTelemetry(runner.TelemetryHome(), attnProjectFlag, "error", sessionID, &stats)
		return err
	}
	runner.AppendLifecycleTelemetry(runner.TelemetryHome(), attnProjectFlag, "stopped", sessionID, &stats)
	return nil
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

func runAttnReport(cmd *cobra.Command, _ []string) error {
	if attnQualitySinceFlag < 0 {
		return usageError{errors.New("--since must not be negative")}
	}
	report, err := runner.QualityReport(attnQualitySinceFlag, attnQualityProjectFlag)
	if err != nil {
		return err
	}
	return writeAttnValue(cmd.OutOrStdout(), report)
}

// writeAttnOutput prints a runner result. The JSON is the protocol the agent
// reads, so it is written whether or not --json was given; the flag stays for
// symmetry with the rest of the CLI and for a future human view.
func writeAttnOutput(writer io.Writer, result runner.Output) error {
	return writeAttnValue(writer, result)
}

func writeAttnValue(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	return encoder.Encode(value)
}
