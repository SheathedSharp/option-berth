package cmd

// The setup half of `oberth attn`: status, enable, test, disable and preview.
// These are the only commands that write the owner-only settings file, and the
// only ones that print what would be sent. Everything here is offline except
// `enable` and `test`, which probe once with a synthetic redacted event.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attn"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/runner"
	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/spf13/cobra"
)

var (
	attnEnablePreset    string
	attnEnableEndpoint  string
	attnEnableModel     string
	attnEnableTimeout   time.Duration
	attnKeyStdinFlag    bool
	attnKeyEnvFlag      string
	attnDisableForget   bool
	attnPreviewTaskFlag string
	attnPreviewAuthFlag []string
)

var attnStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether the assessment is on, and where its settings come from",
	Long: "Report the resolved configuration without contacting a provider: on or off,\n" +
		"endpoint, model, where the key came from, and the fixed reason when it cannot\n" +
		"be used. The key itself is never printed, only a short fingerprint.",
	Args: cobra.NoArgs,
	RunE: runAttnStatus,
}

var attnEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Store a key, prove it works, and turn the optional assessment on",
	Long: "One step, in this order: choose an endpoint and model (directly, or through a\n" +
		"preset that fills both), read the key from standard input (--key-stdin), an\n" +
		"environment variable (--key-env NAME), or a hidden terminal prompt; send one\n" +
		"synthetic redacted event to prove the key answers; only then write the\n" +
		"owner-only attn.json. A probe that fails writes nothing and prints the fixed\n" +
		"reason, so a typo cannot leave a configuration that breaks every task.\n\n" +
		"The command finishes by printing exactly what will and will not be sent.",
	Args: cobra.NoArgs,
	RunE: runAttnEnable,
}

var attnTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Send one synthetic event and report the fixed reason if it fails",
	Long: "Check the current configuration with one network call. It does not change any\n" +
		"setting, and it does not need a running service or a worktree.",
	Args: cobra.NoArgs,
	RunE: runAttnTest,
}

var attnDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Stop using the assessment but keep the stored key",
	Long: "Turn the assessment off. The key stays in attn.json unless --forget is given, so\n" +
		"enabling again does not need it typed a second time. --forget removes it.",
	Args: cobra.NoArgs,
	RunE: runAttnDisable,
}

var attnPreviewCmd = &cobra.Command{
	Use:   "preview",
	Short: "Print the redacted request body for this worktree, without sending it",
	Long: "Show exactly what would cross the boundary for the current status event: the\n" +
		"worktree hash, the event summary, the evidence references and the fixed option\n" +
		"IDs. It never contacts a provider and never includes logs, paths or the key.",
	Args: cobra.NoArgs,
	RunE: runAttnPreview,
}

func init() {
	attnEnableCmd.Flags().StringVar(&attnEnablePreset, "preset", "",
		"Fill endpoint and model from a preset ("+model.PresetNames()+"); optional")
	attnEnableCmd.Flags().StringVar(&attnEnableEndpoint, "endpoint", "", "Endpoint URL (required unless --preset fills it)")
	attnEnableCmd.Flags().StringVar(&attnEnableModel, "model", "", "Model name (required unless --preset fills it)")
	attnEnableCmd.Flags().DurationVar(&attnEnableTimeout, "timeout", 0, "Request timeout (default 10s)")
	attnEnableCmd.Flags().BoolVar(&attnKeyStdinFlag, "key-stdin", false,
		"Read the key from the first line of standard input")
	attnEnableCmd.Flags().StringVar(&attnKeyEnvFlag, "key-env", "",
		"Read the key from this environment variable")

	attnDisableCmd.Flags().BoolVar(&attnDisableForget, "forget", false, "Also delete the stored key")

	attnPreviewCmd.Flags().StringVar(&attnPreviewTaskFlag, "task", "",
		"Task summary to include (defaults to a generic one)")
	attnPreviewCmd.Flags().StringSliceVar(&attnPreviewAuthFlag, "authorize", nil,
		"Authorized option IDs to include")

	attnCmd.AddCommand(attnStatusCmd, attnEnableCmd, attnTestCmd, attnDisableCmd, attnPreviewCmd)
}

// attnPrint prints the document a script reads, or a short human block. The
// default is human: these commands are run once by a person during setup, and
// the JSON forms exist for the client and for tests.
func attnPrint(value any, human func() string) error {
	if attnJSONFlag {
		return writeAttnValue(os.Stdout, value)
	}
	fmt.Fprintln(os.Stdout, human())
	return nil
}

func runAttnStatus(cmd *cobra.Command, _ []string) error {
	consent := attnResolvedConsent()
	state := consent.RedactedState()
	cfg, present, err := model.LoadLocalConfig()
	state["settings_present"] = present
	state["settings_valid"] = err == nil
	if present && err == nil {
		if fingerprint := model.KeyFingerprint(cfg.APIKey); fingerprint != "" {
			state["key_fingerprint"] = fingerprint
		}
	}
	return attnPrint(state, func() string {
		var b strings.Builder
		if consent.Enabled {
			fmt.Fprintf(&b, "Attention: on (%s)\n", consent.ConsentSource)
		} else {
			fmt.Fprintf(&b, "Attention: off (%s)\n", firstNonEmptyString(consent.Reason, model.ReasonDisabled))
		}
		if consent.Preset != "" {
			fmt.Fprintf(&b, "  preset    %s\n", consent.Preset)
		}
		fmt.Fprintf(&b, "  endpoint  %s\n", consent.Endpoint)
		fmt.Fprintf(&b, "  model     %s\n", consent.Model)
		fmt.Fprintf(&b, "  key       %s\n", keySourceLine(consent.KeySource, state["key_fingerprint"]))
		if !consent.Enabled {
			fmt.Fprintf(&b, "  next      %s\n", model.ReasonHint(consent.Reason))
		}
		return strings.TrimRight(b.String(), "\n")
	})
}

func keySourceLine(source model.Source, fingerprint any) string {
	if source == model.SourceNone {
		return "not configured"
	}
	if text, ok := fingerprint.(string); ok && text != "" {
		return fmt.Sprintf("%s (…%s)", source, text)
	}
	return string(source)
}

func runAttnEnable(cmd *cobra.Command, _ []string) error {
	if attnKeyStdinFlag && attnKeyEnvFlag != "" {
		return usageError{errors.New("--key-stdin and --key-env are mutually exclusive")}
	}
	current, _, err := model.LoadLocalConfig()
	if err != nil {
		// A malformed file is replaceable: enable is the repair path.
		current = model.LocalConfig{}
	}
	options := model.EnableOptions{
		Preset: attnEnablePreset, Endpoint: attnEnableEndpoint, Model: attnEnableModel,
		Timeout: attnEnableTimeout, KeyKind: model.KeyFromPrompt,
	}
	// Validate the endpoint and model before asking for a secret: a person who
	// forgot --preset should not type a key only to be told the shape is wrong.
	next := options.SettingsFrom(current)
	if err := next.Validate(); err != nil {
		return usageError{err}
	}
	switch {
	case attnKeyStdinFlag:
		options.KeyKind = model.KeyFromStdin
	case attnKeyEnvFlag != "":
		options.KeyKind = model.KeyFromEnv
		options.KeyEnv = attnKeyEnvFlag
	default:
		key, err := readSecretFromTerminal()
		if err != nil {
			return err
		}
		options.KeyValue = key
	}
	key, err := options.ResolveKey(os.Getenv, func() (string, error) {
		return bufio.NewReader(os.Stdin).ReadString('\n')
	})
	if err != nil {
		return err
	}
	next.APIKey = key
	if err := next.Validate(); err != nil {
		return usageError{err}
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), next.Timeout()+time.Second)
	defer cancel()
	probe := model.ProbeOnce(ctx, next)
	if !probe.OK {
		// Nothing is written: a configuration that does not work must not
		// replace one that might.
		return failHint("attn_probe_failed",
			fmt.Sprintf("the provider did not accept this key (%s)", probe.Reason),
			probe.Text)
	}
	path := paths.AttnConfigPath()
	if err := model.WriteSettings(path, next); err != nil {
		return err
	}
	state := model.Resolved{
		Enabled: true, ConsentSource: model.SourceSettings, Preset: next.Preset,
		Endpoint: next.Endpoint, Model: next.Model, Key: next.APIKey,
		KeySource: model.SourceSettings, Timeout: next.Timeout(),
	}
	return attnPrint(map[string]any{
		"status":   "enabled",
		"path":     path,
		"settings": state.RedactedState(),
		"probe":    map[string]any{"ok": true, "model": probe.Model, "latency_ms": probe.Latency.Milliseconds()},
		"sends":    model.SendsSummary(),
	}, func() string {
		return fmt.Sprintf("Attention: enabled (%s, key %s)\n  %s\n\n%s",
			next.Model, keySourceLine(model.SourceSettings, model.KeyFingerprint(next.APIKey)),
			path, model.HumanSendsSummary())
	})
}

func runAttnTest(cmd *cobra.Command, _ []string) error {
	consent := attnResolvedConsent()
	if !consent.Enabled {
		return failHint("attn_disabled", "the assessment is off", model.ReasonHint(firstNonEmptyString(consent.Reason, model.ReasonDisabled)))
	}
	cfg, _, err := model.LoadLocalConfig()
	if err != nil {
		return err
	}
	// The probe must use the same effective fields a real assessment would:
	// environment overrides are already merged into the resolved consent.
	cfg.Endpoint, cfg.Model, cfg.APIKey = consent.Endpoint, consent.Model, consent.Key
	if cfg.TimeoutMS == 0 {
		cfg.TimeoutMS = int(consent.Timeout / time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), consent.Timeout+time.Second)
	defer cancel()
	probe := model.ProbeOnce(ctx, cfg)
	document := map[string]any{
		"status": "ok", "endpoint": consent.Endpoint, "model": probe.Model,
		"latency_ms": probe.Latency.Milliseconds(),
	}
	if !probe.OK {
		document["status"] = "unavailable"
		document["reason"] = probe.Reason
		document["hint"] = probe.Text
	}
	if err := attnPrint(document, func() string {
		if probe.OK {
			return fmt.Sprintf("Attention: the provider answered (%s, %dms)", probe.Model, probe.Latency.Milliseconds())
		}
		return fmt.Sprintf("Attention: unavailable (%s)\n  next: %s", probe.Reason, probe.Text)
	}); err != nil {
		return err
	}
	if !probe.OK {
		return errSilent
	}
	return nil
}

func runAttnDisable(cmd *cobra.Command, _ []string) error {
	cfg, present, err := model.LoadLocalConfig()
	if err != nil {
		cfg = model.LocalConfig{}
	}
	if !present && cfg.APIKey == "" && !attnDisableForget {
		// Nothing to turn off. Saying so is better than writing a file that
		// looks like a decision nobody made.
		return attnPrint(map[string]any{"status": "already_off"}, func() string {
			return "Attention: already off"
		})
	}
	path := paths.AttnConfigPath()
	if err := model.DisableSettings(path, cfg, attnDisableForget); err != nil {
		return err
	}
	status := "disabled"
	if attnDisableForget {
		status = "disabled_and_forgotten"
	}
	return attnPrint(map[string]any{"status": status, "path": path, "key_kept": !attnDisableForget},
		func() string {
			if attnDisableForget {
				return "Attention: disabled, stored key removed"
			}
			return "Attention: disabled, stored key kept (use --forget to remove it)"
		})
}

// runAttnPreview builds the request the adapter would send for the current
// worktree event and prints it. It is the honesty test a person runs before
// enabling: if what is shown is not acceptable, do not enable.
func runAttnPreview(cmd *cobra.Command, _ []string) error {
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
	task := model.TaskContext{
		Task: firstNonEmptyString(attnPreviewTaskFlag,
			"Describe what an assessment would send for this event"),
		AuthorizedOptions: attnPreviewAuthFlag,
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

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
