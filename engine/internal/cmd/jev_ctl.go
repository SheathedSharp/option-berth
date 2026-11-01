package cmd

// The setup half of `oberth jev`: status, enable, test, disable and preview.
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

	"github.com/sheathedsharp/option-berth/engine/internal/attention"
	"github.com/sheathedsharp/option-berth/engine/internal/jev"
	"github.com/sheathedsharp/option-berth/engine/internal/jev/runner"
	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/spf13/cobra"
)

var (
	jevEnableProvider  string
	jevEnableEndpoint  string
	jevEnableModel     string
	jevEnableTimeout   time.Duration
	jevKeyStdinFlag    bool
	jevKeyEnvFlag      string
	jevDisableForget   bool
	jevPreviewTaskFlag string
	jevPreviewAuthFlag []string
)

var jevStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether Jev is on, and where its settings come from",
	Long: "Report the resolved configuration without contacting a provider: on or off,\n" +
		"which provider and endpoint, where the key came from, and the fixed reason when\n" +
		"it cannot be used. The key itself is never printed, only a short fingerprint.",
	Args: cobra.NoArgs,
	RunE: runJevStatus,
}

var jevEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Store a key, prove it works, and turn the optional assessment on",
	Long: "One step, in this order: choose a provider, read the key from standard input\n" +
		"(--key-stdin), an environment variable (--key-env NAME), or a hidden terminal\n" +
		"prompt; send one synthetic redacted event to prove the key answers; only then\n" +
		"write the owner-only jev.json. A probe that fails writes nothing and prints the\n" +
		"fixed reason, so a typo cannot leave a configuration that breaks every task.\n\n" +
		"The command finishes by printing exactly what will and will not be sent.",
	Args: cobra.NoArgs,
	RunE: runJevEnable,
}

var jevTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Send one synthetic event and report the fixed reason if it fails",
	Long: "Check the current configuration with one network call. It does not change any\n" +
		"setting, and it does not need a running service or a worktree.",
	Args: cobra.NoArgs,
	RunE: runJevTest,
}

var jevDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Stop using Jev but keep the stored key",
	Long: "Turn the assessment off. The key stays in jev.json unless --forget is given, so\n" +
		"enabling again does not need it typed a second time. --forget removes it.",
	Args: cobra.NoArgs,
	RunE: runJevDisable,
}

var jevPreviewCmd = &cobra.Command{
	Use:   "preview",
	Short: "Print the redacted request body for this worktree, without sending it",
	Long: "Show exactly what would cross the boundary for the current status event: the\n" +
		"worktree hash, the event summary, the evidence references and the fixed option\n" +
		"IDs. It never contacts a provider and never includes logs, paths or the key.",
	Args: cobra.NoArgs,
	RunE: runJevPreview,
}

func init() {
	jevEnableCmd.Flags().StringVar(&jevEnableProvider, "provider", "openrouter",
		"Provider: openrouter or typesafe")
	jevEnableCmd.Flags().StringVar(&jevEnableEndpoint, "endpoint", "", "Override the provider endpoint")
	jevEnableCmd.Flags().StringVar(&jevEnableModel, "model", "", "Override the provider model")
	jevEnableCmd.Flags().DurationVar(&jevEnableTimeout, "timeout", 0, "Request timeout (default 10s)")
	jevEnableCmd.Flags().BoolVar(&jevKeyStdinFlag, "key-stdin", false,
		"Read the key from the first line of standard input")
	jevEnableCmd.Flags().StringVar(&jevKeyEnvFlag, "key-env", "",
		"Read the key from this environment variable")

	jevDisableCmd.Flags().BoolVar(&jevDisableForget, "forget", false, "Also delete the stored key")

	jevPreviewCmd.Flags().StringVar(&jevPreviewTaskFlag, "task", "",
		"Task summary to include (defaults to a generic one)")
	jevPreviewCmd.Flags().StringSliceVar(&jevPreviewAuthFlag, "authorize", nil,
		"Authorized option IDs to include")

	jevCmd.AddCommand(jevStatusCmd, jevEnableCmd, jevTestCmd, jevDisableCmd, jevPreviewCmd)
}

// jevJSON prints the document a script reads, or a short human block. The
// default is human: these commands are run once by a person during setup, and
// the JSON forms exist for the client and for tests.
func jevPrint(value any, human func() string) error {
	if jevJSONFlag {
		return writeJevValue(os.Stdout, value)
	}
	fmt.Fprintln(os.Stdout, human())
	return nil
}

func runJevStatus(cmd *cobra.Command, _ []string) error {
	consent := jevResolvedConsent()
	state := consent.RedactedState()
	cfg, present, err := jev.LoadLocalJevConfig()
	state["settings_present"] = present
	state["settings_valid"] = err == nil
	if present && err == nil {
		if fingerprint := jev.KeyFingerprint(cfg.APIKey); fingerprint != "" {
			state["key_fingerprint"] = fingerprint
		}
	}
	return jevPrint(state, func() string {
		var b strings.Builder
		if consent.Enabled {
			fmt.Fprintf(&b, "Jev: on (%s)\n", consent.ConsentSource)
		} else {
			fmt.Fprintf(&b, "Jev: off (%s)\n", firstNonEmptyString(consent.Reason, jev.ReasonDisabled))
		}
		fmt.Fprintf(&b, "  provider  %s\n", consent.Provider)
		fmt.Fprintf(&b, "  endpoint  %s\n", consent.Endpoint)
		fmt.Fprintf(&b, "  model     %s\n", consent.Model)
		fmt.Fprintf(&b, "  key       %s\n", keySourceLine(consent.KeySource, state["key_fingerprint"]))
		if !consent.Enabled {
			fmt.Fprintf(&b, "  next      %s\n", jev.ReasonHint(consent.Reason))
		}
		return strings.TrimRight(b.String(), "\n")
	})
}

func keySourceLine(source jev.Source, fingerprint any) string {
	if source == jev.SourceNone {
		return "not configured"
	}
	if text, ok := fingerprint.(string); ok && text != "" {
		return fmt.Sprintf("%s (…%s)", source, text)
	}
	return string(source)
}

func runJevEnable(cmd *cobra.Command, _ []string) error {
	if jevKeyStdinFlag && jevKeyEnvFlag != "" {
		return usageError{errors.New("--key-stdin and --key-env are mutually exclusive")}
	}
	current, _, err := jev.LoadLocalJevConfig()
	if err != nil {
		// A malformed file is replaceable: enable is the repair path.
		current = jev.LocalJevConfig{}
	}
	options := jev.EnableOptions{
		Provider: jevEnableProvider, Endpoint: jevEnableEndpoint, Model: jevEnableModel,
		Timeout: jevEnableTimeout, KeyKind: jev.KeyFromPrompt,
	}
	switch {
	case jevKeyStdinFlag:
		options.KeyKind = jev.KeyFromStdin
	case jevKeyEnvFlag != "":
		options.KeyKind = jev.KeyFromEnv
		options.KeyEnv = jevKeyEnvFlag
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
	next := options.SettingsFrom(current)
	next.APIKey = key
	if err := next.Validate(); err != nil {
		return usageError{err}
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), next.Timeout()+time.Second)
	defer cancel()
	probe := jev.ProbeOnce(ctx, next)
	if !probe.OK {
		// Nothing is written: a configuration that does not work must not
		// replace one that might.
		return failHint("jev_probe_failed",
			fmt.Sprintf("the provider did not accept this key (%s)", probe.Reason),
			probe.Text)
	}
	path := paths.JevConfigPath()
	if err := jev.WriteSettings(path, next); err != nil {
		return err
	}
	state := jev.Resolved{
		Enabled: true, ConsentSource: jev.SourceSettings, Provider: next.Provider,
		Endpoint: next.Endpoint, Model: next.Model, Key: next.APIKey,
		KeySource: jev.SourceSettings, Timeout: next.Timeout(),
	}
	return jevPrint(map[string]any{
		"status":   "enabled",
		"path":     path,
		"settings": state.RedactedState(),
		"probe":    map[string]any{"ok": true, "model": probe.Model, "latency_ms": probe.Latency.Milliseconds()},
		"sends":    jev.SendsSummary(),
	}, func() string {
		return fmt.Sprintf("Jev: enabled (%s, key %s)\n  %s\n\n%s",
			next.Provider, keySourceLine(jev.SourceSettings, jev.KeyFingerprint(next.APIKey)),
			path, jev.HumanSendsSummary())
	})
}

func runJevTest(cmd *cobra.Command, _ []string) error {
	consent := jevResolvedConsent()
	if !consent.Enabled {
		return failHint("jev_disabled", "Jev is off", jev.ReasonHint(firstNonEmptyString(consent.Reason, jev.ReasonDisabled)))
	}
	cfg, _, err := jev.LoadLocalJevConfig()
	if err != nil {
		return err
	}
	if consent.KeySource == jev.SourceEnvironment || consent.KeySource == jev.SourceFlag {
		cfg.APIKey = consent.Key
	}
	if cfg.TimeoutMS == 0 {
		cfg.TimeoutMS = int(consent.Timeout / time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), consent.Timeout+time.Second)
	defer cancel()
	probe := jev.ProbeOnce(ctx, cfg)
	document := map[string]any{
		"status": "ok", "provider": consent.Provider, "model": probe.Model,
		"latency_ms": probe.Latency.Milliseconds(),
	}
	if !probe.OK {
		document["status"] = "unavailable"
		document["reason"] = probe.Reason
		document["hint"] = probe.Text
	}
	if err := jevPrint(document, func() string {
		if probe.OK {
			return fmt.Sprintf("Jev: the provider answered (%s, %dms)", probe.Model, probe.Latency.Milliseconds())
		}
		return fmt.Sprintf("Jev: unavailable (%s)\n  next: %s", probe.Reason, probe.Text)
	}); err != nil {
		return err
	}
	if !probe.OK {
		return errSilent
	}
	return nil
}

func runJevDisable(cmd *cobra.Command, _ []string) error {
	cfg, present, err := jev.LoadLocalJevConfig()
	if err != nil {
		cfg = jev.LocalJevConfig{}
	}
	if !present && cfg.APIKey == "" && !jevDisableForget {
		// Nothing to turn off. Saying so is better than writing a file that
		// looks like a decision nobody made.
		return jevPrint(map[string]any{"status": "already_off"}, func() string {
			return "Jev: already off"
		})
	}
	path := paths.JevConfigPath()
	if err := jev.DisableSettings(path, cfg, jevDisableForget); err != nil {
		return err
	}
	status := "disabled"
	if jevDisableForget {
		status = "disabled_and_forgotten"
	}
	return jevPrint(map[string]any{"status": status, "path": path, "key_kept": !jevDisableForget},
		func() string {
			if jevDisableForget {
				return "Jev: disabled, stored key removed"
			}
			return "Jev: disabled, stored key kept (use --forget to remove it)"
		})
}

// runJevPreview builds the request the adapter would send for the current
// worktree event and prints it. It is the honesty test a person runs before
// enabling: if what is shown is not acceptable, do not enable.
func runJevPreview(cmd *cobra.Command, _ []string) error {
	status, err := runner.ReadStatus(cmd.Context(), jevOberthFlag, jevProjectFlag)
	if err != nil {
		return err
	}
	if status.AttentionPath == "" {
		return jevPrint(map[string]any{"status": "clear"}, func() string {
			return "No current anomaly, so there is nothing to assess."
		})
	}
	artifact, err := attention.ReadFresh(status.AttentionPath, time.Time{})
	if err != nil {
		return failHint("stale", "the attention artifact is stale", jev.ReasonHint(jev.ReasonStale))
	}
	task := jev.TaskContext{
		Task: firstNonEmptyString(jevPreviewTaskFlag,
			"Describe what an assessment would send for this event"),
		AuthorizedOptions: jevPreviewAuthFlag,
	}
	if len(task.AuthorizedOptions) == 0 {
		for _, option := range artifact.Options {
			task.AuthorizedOptions = append(task.AuthorizedOptions, option.ID)
		}
	}
	body, err := jev.PreviewRequest(artifact, task)
	if err != nil {
		return usageError{err}
	}
	return jevPrint(map[string]any{"status": "preview", "request": body}, func() string {
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
