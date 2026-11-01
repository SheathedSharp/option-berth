package cmd

// `oberth jev` carries the attention runner that used to live only in the
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

	"github.com/sheathedsharp/option-berth/engine/internal/jev"
	"github.com/sheathedsharp/option-berth/engine/internal/jev/runner"
	"github.com/spf13/cobra"
)

// The flags of the whole `jev` family. They are package variables for the same
// reason every other command's are: the tree is built once per process.
var (
	jevJSONFlag      bool
	jevProjectFlag   string
	jevTaskFlag      string
	jevTaskFileFlag  string
	jevAuthorizeFlag []string
	jevIntervalFlag  time.Duration
	jevOberthFlag    string

	jevFeedbackFileFlag   string
	jevAttentionFileFlag  string
	jevRawFlag            bool
	jevQualitySinceFlag   time.Duration
	jevQualityProjectFlag string
)

var jevCmd = &cobra.Command{
	Use:     "jev",
	Short:   "Optional model assessment of a runtime anomaly (off unless enabled)",
	GroupID: commandGroupHandoff,
	Long: "Ask an optional model whether a runtime anomaly affects the current task, and\n" +
		"whether the next step needs the person's choice.\n\n" +
		"Off by default. Only `enabled: true` in the owner-only jev.json under\n" +
		"BERTH_HOME, or a one-shot BERTH_JEV=1 for this process, lets this command\n" +
		"contact a provider; a key in the environment supplies the secret but is not\n" +
		"consent. `oberth status` only publishes `attention_path` when Jev is enabled,\n" +
		"so its presence already means the person opted in.\n\n" +
		"The command never starts, stops or restarts a service. It reads\n" +
		"`oberth status --json`, validates the published artifact against that exact\n" +
		"revision, sends a redacted summary, and prints one JSON value when Jev cannot\n" +
		"answer, the reason is one fixed code (`no_key`, `disabled`, `unauthorized`,\n" +
		"`rate_limited`, `timeout`, `network`, `provider_error`, `bad_response`, `stale`).",
}

var jevAssessCmd = &cobra.Command{
	Use:   "assess",
	Short: "One status read, one anomaly assessment, one handoff",
	Long: "Read `oberth status --json`, verify the published artifact still matches that\n" +
		"revision, ask the provider, and print the handoff. The authorized option IDs\n" +
		"are the agent's explicit allow-list; artifact options are candidates, never\n" +
		"permission. This is the former `jev-attention --auto`.",
	Args: cobra.NoArgs,
	RunE: runJevAssess,
}

var jevSessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Keep watching for a task and emit only changed handoffs",
	Long: "Poll `oberth status --json` for the lifetime of a task and emit NDJSON: one\n" +
		"`subscribed` record, then only changed event/revision/assessment handoffs, and\n" +
		"`stopped` when the process is cancelled. Repeated polls of unchanged facts are\n" +
		"deliberately silent.",
	Args: cobra.NoArgs,
	RunE: runJevSession,
}

var jevFeedbackCmd = &cobra.Command{
	Use:   "feedback",
	Short: "Record what a person actually decided about one handoff",
	Long: "Append one agent quality-feedback JSON document to the local ledger. This is\n" +
		"the label that makes the ledger answer 'was a person needed'; it never changes\n" +
		"the attention artifact and never grants permission.",
	Args: cobra.NoArgs,
	RunE: runJevFeedback,
}

var jevReportCmd = &cobra.Command{
	Use:   "report",
	Short: "Summarize local attention quality as JSON",
	Long: "Read the local quality ledger and report coverage, assessment availability,\n" +
		"routing reasons, and the labelled precision/recall of the handoff policy. A\n" +
		"metric stays null until there are enough real labels; it is never zero-filled.",
	Args: cobra.NoArgs,
	RunE: runJevReport,
}

func init() {
	jevCmd.PersistentFlags().BoolVar(&jevJSONFlag, "json", false, "Output as JSON")
	jevCmd.PersistentFlags().StringVar(&jevProjectFlag, "project", ".", "Worktree directory to read")
	jevCmd.PersistentFlags().StringVar(&jevOberthFlag, "oberth", "oberth", "oberth executable to read status with")

	jevCmd.PersistentFlags().StringVar(&jevTaskFlag, "task", "", "Task summary (alternative to --task-context)")
	jevCmd.PersistentFlags().StringVar(&jevTaskFileFlag, "task-context", "",
		"JSON file containing task and authorized_options")
	jevCmd.PersistentFlags().StringSliceVar(&jevAuthorizeFlag, "authorize", nil,
		"Authorized option IDs for this task (comma separated); overrides the context file")
	jevSessionCmd.Flags().DurationVar(&jevIntervalFlag, "interval", 2*time.Second, "Poll interval")

	jevFeedbackCmd.Flags().StringVar(&jevFeedbackFileFlag, "file", "", "Feedback JSON document to append")
	jevFeedbackCmd.Flags().StringVar(&jevAttentionFileFlag, "attention", "",
		"Attention artifact the feedback refers to, to check its identity")
	_ = jevFeedbackCmd.MarkFlagRequired("file")

	jevReportCmd.Flags().DurationVar(&jevQualitySinceFlag, "since", 0, "Lookback window (for example 168h)")
	jevReportCmd.Flags().StringVar(&jevQualityProjectFlag, "quality-project", "",
		"Project label or worktree id to filter on")

	jevCmd.AddCommand(jevAssessCmd, jevSessionCmd, jevFeedbackCmd, jevReportCmd)
	rootCmd.AddCommand(jevCmd)
}

// jevTaskContext loads the agent's explicit authorization. There is no default:
// an unset allow-list is an error, because a candidate option in an artifact is
// not permission to act on it.
func jevTaskContext() (jev.TaskContext, error) {
	var task jev.TaskContext
	var err error
	if strings.TrimSpace(jevTaskFileFlag) != "" {
		task, err = jev.ReadTaskContext(jevTaskFileFlag)
		if err != nil {
			return jev.TaskContext{}, err
		}
	}
	if strings.TrimSpace(jevTaskFlag) != "" {
		task.Task = jevTaskFlag
	}
	if len(jevAuthorizeFlag) > 0 {
		task.AuthorizedOptions = append([]string(nil), jevAuthorizeFlag...)
	}
	if strings.TrimSpace(task.Task) == "" {
		return task, usageError{errors.New("--task or --task-context is required")}
	}
	if len(task.AuthorizedOptions) == 0 {
		return task, usageError{errors.New("--authorize (or authorized_options) is required")}
	}
	return task, nil
}

// jevResolvedConsent is the same single resolution the standalone command uses.
// A reason means "do not call the provider", never "guess a default".
func jevResolvedConsent() jev.Resolved {
	input := jev.ResolveInputFromEnvironment()
	input.Settings, input.SettingsPresent, input.SettingsErr = jev.LoadLocalJevConfig()
	legacy, err := jev.ReadLegacyJevKey()
	input.LegacyKey, input.LegacyErr = legacy, err
	input.LegacyFound = strings.TrimSpace(legacy) != ""
	return jev.Resolve(input)
}

func runJevAssess(cmd *cobra.Command, _ []string) error {
	task, err := jevTaskContext()
	if err != nil {
		return err
	}
	consent := jevResolvedConsent()
	started := time.Now()
	// Jev being off does not mean "no handoff": the runner still reads status
	// and builds the fact-only request, then reports the fixed reason. Only the
	// provider call is skipped, which is what the person refused.
	client, clientErr := consent.Client()
	if clientErr != nil {
		// The client is empty, so the runner treats this as "no key": it never
		// calls out, and the reason it prints is the consent decision.
		client = jev.Client{}
	}
	result, err := runner.Auto(cmd.Context(), jevOberthFlag, jevProjectFlag, client, task, runner.ReadStatus)
	if err != nil {
		return err
	}
	if consent.Reason != "" {
		result.Status, result.Reason = "unavailable", consent.Reason
	}
	telemetryPath := runner.AppendRunnerTelemetry(runner.TelemetryHome(), jevProjectFlag, task, result, err, "runner", time.Since(started))
	result.TelemetryPath = telemetryPath
	return writeJevOutput(cmd.OutOrStdout(), result)
}

func runJevSession(cmd *cobra.Command, _ []string) error {
	task, err := jevTaskContext()
	if err != nil {
		return err
	}
	if jevIntervalFlag <= 0 {
		return usageError{errors.New("--interval must be positive")}
	}
	consent := jevResolvedConsent()
	// A session on a disabled configuration is not a silent no-op: it reports
	// the fixed reason and stops, so an agent does not wait for events that can
	// never arrive.
	client, clientErr := consent.Client()
	if clientErr != nil {
		return writeJevOutput(cmd.OutOrStdout(), runner.Output{Status: "unavailable", Reason: consent.ReasonOr(clientErr)})
	}
	subscriptionContext, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sessionID := runner.NewSessionID(jevProjectFlag)
	runner.AppendLifecycleTelemetry(runner.TelemetryHome(), jevProjectFlag, "subscribed", sessionID, nil)
	stats, err := runner.SubscribeWithStats(subscriptionContext, jevOberthFlag, jevProjectFlag, jevIntervalFlag,
		client, task, runner.ReadStatus, sessionID,
		func(result runner.Output, latency time.Duration) {
			runner.AppendRunnerTelemetry(runner.TelemetryHome(), jevProjectFlag, task, result, nil, "subscription", latency)
		},
		func(value any) error { return writeJevValue(cmd.OutOrStdout(), value) })
	if err != nil {
		runner.AppendLifecycleTelemetry(runner.TelemetryHome(), jevProjectFlag, "error", sessionID, &stats)
		return err
	}
	runner.AppendLifecycleTelemetry(runner.TelemetryHome(), jevProjectFlag, "stopped", sessionID, &stats)
	return nil
}

func runJevFeedback(cmd *cobra.Command, _ []string) error {
	result, err := runner.Feedback(jevFeedbackFileFlag, jevAttentionFileFlag, jevProjectFlag)
	if err != nil {
		return err
	}
	return writeJevValue(cmd.OutOrStdout(), map[string]string{
		"status": "feedback_recorded", "telemetry_path": result.Path, "request_id": result.RecordID,
	})
}

func runJevReport(cmd *cobra.Command, _ []string) error {
	if jevQualitySinceFlag < 0 {
		return usageError{errors.New("--since must not be negative")}
	}
	report, err := runner.QualityReport(jevQualitySinceFlag, jevQualityProjectFlag)
	if err != nil {
		return err
	}
	return writeJevValue(cmd.OutOrStdout(), report)
}

// writeJevOutput prints a runner result. The JSON is the protocol the agent
// reads, so it is written whether or not --json was given; the flag stays for
// symmetry with the rest of the CLI and for a future human view.
func writeJevOutput(writer io.Writer, result runner.Output) error {
	return writeJevValue(writer, result)
}

func writeJevValue(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	return encoder.Encode(value)
}
