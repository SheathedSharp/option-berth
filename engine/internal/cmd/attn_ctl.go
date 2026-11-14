package cmd

// The person-facing half of `oberth attn`: status, config, on, off and test.
// `config` is the only verb that writes credentials, and it probes once before
// writing; `on` and `off` flip one boolean and never touch the network.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/runner"
	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/sheathedsharp/option-berth/engine/internal/terminal"
	"github.com/spf13/cobra"
)

var (
	attnConfigPreset   string
	attnConfigEndpoint string
	attnConfigModel    string
	attnConfigTimeout  time.Duration
	attnKeyStdinFlag   bool
	attnKeyEnvFlag     string
	attnOffForgetFlag  bool
)

var attnStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the resolved assessment state and its local quality data",
	Long: "Report the resolved configuration without contacting a provider: on or off,\n" +
		"endpoint, model, where the key came from, the fixed reason when it cannot be\n" +
		"used, and a summary of the local quality ledger. The key itself is never\n" +
		"printed, only a short fingerprint.",
	Args: cobra.NoArgs,
	RunE: runAttnStatus,
}

var attnConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Store the endpoint, model and key, proving the key answers",
	Long: "Write the owner-only attn.json. With no flags on a terminal it asks for the\n" +
		"provider, then the model, then the key; every list ends with a custom row, so\n" +
		"an endpoint or model this build has not heard of is one answer away. Give\n" +
		"--preset or --endpoint/--model to skip the questions; the key then comes from\n" +
		"standard input (--key-stdin), an environment variable (--key-env NAME), a\n" +
		"hidden terminal prompt, or the already stored key when no key flag is given.\n" +
		"One synthetic redacted request proves the configuration before anything is\n" +
		"written; a failed probe leaves the existing file untouched. This verb changes\n" +
		"credentials, not consent: it never turns the assessment on.",
	Args: cobra.NoArgs,
	RunE: runAttnConfig,
}

var attnOnCmd = &cobra.Command{
	Use:   "on",
	Short: "Turn the assessment on (offline, one boolean)",
	Long: "Set \"enabled\": true in attn.json. This is a consent flip, not a probe: run\n" +
		"`oberth attn config` first to store a working configuration, and\n" +
		"`oberth attn test` afterwards to verify it.",
	Args: cobra.NoArgs,
	RunE: runAttnOn,
}

var attnOffCmd = &cobra.Command{
	Use:   "off",
	Short: "Turn the assessment off but keep the stored key",
	Long: "Set \"enabled\": false in attn.json. The key stays unless --forget is given, so\n" +
		"turning it back on does not need it typed again.",
	Args: cobra.NoArgs,
	RunE: runAttnOff,
}

var attnTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Send one synthetic event and report the fixed reason if it fails",
	Long: "Check the current configuration with one network call. It does not change any\n" +
		"setting, and it does not need a running service or a worktree.",
	Args: cobra.NoArgs,
	RunE: runAttnTest,
}

func init() {
	attnConfigCmd.Flags().StringVar(&attnConfigPreset, "preset", "",
		"Fill endpoint and model from a preset ("+model.PresetNames()+"); optional")
	attnConfigCmd.Flags().StringVar(&attnConfigEndpoint, "endpoint", "", "Endpoint URL (required unless --preset fills it)")
	attnConfigCmd.Flags().StringVar(&attnConfigModel, "model", "", "Model name (required unless --preset fills it)")
	attnConfigCmd.Flags().DurationVar(&attnConfigTimeout, "timeout", 0, "Request timeout (default 10s)")
	attnConfigCmd.Flags().BoolVar(&attnKeyStdinFlag, "key-stdin", false,
		"Read the key from the first line of standard input")
	attnConfigCmd.Flags().StringVar(&attnKeyEnvFlag, "key-env", "",
		"Read the key from this environment variable")

	attnOffCmd.Flags().BoolVar(&attnOffForgetFlag, "forget", false, "Also delete the stored key")
}

// attnPrint prints the document a script reads, or a short human block. Setup
// verbs are run by a person, so the default is human; JSON exists for the client
// and for tests.
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
	if attnStatusSinceFlag < 0 {
		return usageError{errors.New("--since must not be negative")}
	}
	report, err := runner.QualityReport(attnStatusSinceFlag, attnQualityProjectFlag)
	if err != nil {
		return err
	}
	state["quality"] = report
	return attnPrint(state, func() string {
		var b strings.Builder
		switch {
		case consent.Enabled:
			fmt.Fprintf(&b, "Attention: on (%s; assessment allowed, not an automatic session)\n", consent.ConsentSource)
		case !present:
			// An empty file and an empty endpoint/model table say nothing a
			// reader can act on; name the state and the one next step instead.
			b.WriteString("Attention: not configured\n")
		default:
			fmt.Fprintf(&b, "Attention: off (%s)\n", firstNonEmptyString(consent.Reason, model.ReasonDisabled))
		}
		if consent.Preset != "" {
			fmt.Fprintf(&b, "  preset    %s\n", consent.Preset)
		}
		if consent.Endpoint != "" {
			fmt.Fprintf(&b, "  endpoint  %s\n", consent.Endpoint)
		}
		if consent.Model != "" {
			fmt.Fprintf(&b, "  model     %s\n", consent.Model)
		}
		if present {
			fmt.Fprintf(&b, "  key       %s\n", keySourceLine(consent.KeySource, state["key_fingerprint"]))
		}
		fmt.Fprintf(&b, "  quality   %s\n", qualitySummary(report, attnStatusSinceFlag))
		if consent.Enabled && consent.Reason != "" {
			fmt.Fprintf(&b, "  assessment unavailable (%s)\n", consent.Reason)
		}
		fmt.Fprintf(&b, "  activity  %s\n", model.ActivitySummary(report.Activity))
		if consent.Enabled && consent.Reason == "" && report.Activity.State == "no_session" {
			b.WriteString("  next      attach a task-scoped `oberth attn assess --follow --project <worktree> --task <summary> --authorize <fixed-options>`; no background session was started\n")
		}
		if !consent.Enabled || consent.Reason != "" {
			fmt.Fprintf(&b, "  next      %s\n", model.ReasonHint(consent.Reason))
		}
		return strings.TrimRight(b.String(), "\n")
	})
}

func qualitySummary(report model.QualityReport, since time.Duration) string {
	window := "all"
	if since > 0 {
		window = strings.TrimSuffix(strings.TrimSuffix(since.String(), "0m0s"), "0s")
	}
	bound := ""
	if report.LegacyCountsUnallocated {
		bound = ">="
	}
	parts := []string{
		fmt.Sprintf("window %s", window),
		fmt.Sprintf("assessments %d", report.AssessmentRecords),
		fmt.Sprintf("model calls %s%d", bound, report.ModelCalls),
		fmt.Sprintf("cache hits %s%d", bound, report.CacheHits),
		fmt.Sprintf("labeled %d/%d", report.LabeledCases, report.UniqueCases),
	}
	if report.LegacyCountsUnallocated {
		parts = append(parts, "legacy session totals cannot be allocated to this window")
	}
	if report.HumanDecision.Precision != nil && report.HumanDecision.Recall != nil {
		parts = append(parts, fmt.Sprintf("precision %.2f recall %.2f",
			*report.HumanDecision.Precision, *report.HumanDecision.Recall))
	}
	return strings.Join(parts, " · ")
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

func runAttnConfig(cmd *cobra.Command, _ []string) error {
	if attnKeyStdinFlag && attnKeyEnvFlag != "" {
		return usageError{errors.New("--key-stdin and --key-env are mutually exclusive")}
	}
	// A person at a terminal with no fields to set gets the guided form. Any
	// flag, --json, a pipe or Windows keeps the flag form below, so every
	// scripted path behaves exactly as before.
	if attnConfigWizardApplies() {
		return runAttnConfigWizard(cmd)
	}
	current, present, err := model.LoadLocalConfig()
	if err != nil {
		// A malformed file is replaceable: config is the repair path.
		current = model.LocalConfig{}
	}
	if noAttnConfigFlags() {
		return attnPrint(current.RedactedSettings(), func() string {
			if !present {
				return "Attention: not configured.\n" + attnProviderList()
			}
			return humanSettings(current)
		})
	}
	options := model.EnableOptions{
		Preset: attnConfigPreset, Endpoint: attnConfigEndpoint,
		Model: attnConfigModel, Timeout: attnConfigTimeout,
	}
	next := options.SettingsFrom(current)
	// Validate the endpoint and model before asking for a secret: a person who
	// forgot --preset should not type a key only to be told the shape is wrong.
	if err := attnValidateSelection(next); err != nil {
		return err
	}
	key, keyErr := attnConfigKey(next.APIKey)
	if keyErr != nil {
		return keyErr
	}
	next.APIKey = key
	return attnCommitConfig(cmd, next)
}

// attnCommitConfig is the one end of every setup path: validate, prove the key
// with one synthetic redacted assessment, and only then write. A new way of
// choosing the fields cannot skip the proof, and a failed probe leaves an
// existing configuration untouched.
func attnCommitConfig(cmd *cobra.Command, next model.LocalConfig) error {
	if err := attnValidateSelection(next); err != nil {
		return err
	}
	if strings.TrimSpace(next.APIKey) == "" {
		return usageError{errors.New("an API key is required; rerun `oberth attn config`")}
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), next.Timeout()+time.Second)
	defer cancel()
	probe := model.ProbeOnce(ctx, next)
	if !probe.OK {
		// Nothing is written: a configuration that does not work must not replace
		// one that might.
		return failHint("attn_probe_failed",
			fmt.Sprintf("the endpoint did not accept this key (%s)", probe.Reason), probe.Text)
	}
	path := paths.AttnConfigPath()
	if err := model.WriteSettings(path, next); err != nil {
		return err
	}
	state := model.Resolved{
		Enabled: next.Enabled, ConsentSource: model.SourceSettings, Preset: next.Preset,
		Endpoint: next.Endpoint, Model: next.Model, Key: next.APIKey,
		KeySource: model.SourceSettings, Timeout: next.Timeout(),
	}
	return attnPrint(map[string]any{
		"status":   "configured",
		"path":     path,
		"settings": state.RedactedState(),
		"probe":    map[string]any{"ok": true, "model": probe.Model, "latency_ms": probe.Latency.Milliseconds()},
		"sends":    model.SendsSummary(),
	}, func() string {
		var b strings.Builder
		fmt.Fprintf(&b, "Attention: configured (%s, key %s)\n",
			next.Model, keySourceLine(model.SourceSettings, model.KeyFingerprint(next.APIKey)))
		fmt.Fprintf(&b, "  %s\n", path)
		fmt.Fprintf(&b, "  consent   %s\n\n", attnConsentLine(next.Enabled))
		b.WriteString(model.HumanSendsSummary())
		b.WriteString("\n\n  next      oberth attn test   send one synthetic event")
		if !next.Enabled {
			b.WriteString("\n            oberth attn on     allow assessments")
		}
		return b.String()
	})
}

// attnConsentLine says plainly that a configured provider is not an opt-in.
func attnConsentLine(enabled bool) string {
	if enabled {
		return "on (kept)"
	}
	return "off; `oberth attn on` turns the assessment on"
}

func noAttnConfigFlags() bool {
	return strings.TrimSpace(attnConfigPreset) == "" && strings.TrimSpace(attnConfigEndpoint) == "" &&
		strings.TrimSpace(attnConfigModel) == "" && attnConfigTimeout == 0 &&
		!attnKeyStdinFlag && strings.TrimSpace(attnKeyEnvFlag) == ""
}

// attnConfigKey answers which secret to store. An explicit source wins; no key
// flag keeps the stored key, or prompts when there is none to keep.
func attnConfigKey(stored string) (string, error) {
	switch {
	case attnKeyStdinFlag:
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return "", usageError{errors.New("standard input did not contain a key")}
		}
		return line, nil
	case attnKeyEnvFlag != "":
		value := strings.TrimSpace(os.Getenv(attnKeyEnvFlag))
		if value == "" {
			return "", usageError{fmt.Errorf("environment variable %s is empty", attnKeyEnvFlag)}
		}
		return value, nil
	case strings.TrimSpace(stored) != "":
		return strings.TrimSpace(stored), nil
	default:
		key, err := terminal.ReadSecret(os.Stderr, "API key (input hidden)")
		if errors.Is(err, terminal.ErrNotInteractive) {
			return "", usageError{errors.New("standard input is not a terminal; use --key-stdin or --key-env")}
		}
		if err != nil {
			return "", err
		}
		return key, nil
	}
}

func humanSettings(cfg model.LocalConfig) string {
	var b strings.Builder
	state := "off"
	if cfg.Enabled {
		state = "on"
	}
	fmt.Fprintf(&b, "Attention: %s\n", state)
	if cfg.Preset != "" {
		fmt.Fprintf(&b, "  preset    %s\n", cfg.Preset)
	}
	fmt.Fprintf(&b, "  endpoint  %s\n", cfg.Endpoint)
	fmt.Fprintf(&b, "  model     %s\n", cfg.Model)
	fmt.Fprintf(&b, "  key       %s\n", keySourceLine(model.SourceSettings, model.KeyFingerprint(cfg.APIKey)))
	return strings.TrimRight(b.String(), "\n")
}

func runAttnOn(cmd *cobra.Command, _ []string) error {
	cfg, present, err := model.LoadLocalConfig()
	if err != nil {
		return failHint("attn_invalid_config", "attention settings are not usable",
			"run `oberth attn config` to rewrite them")
	}
	if !present {
		return failHint("attn_not_configured", "attention is not configured",
			"run `oberth attn config` first")
	}
	if cfg.Enabled {
		return attnPrint(map[string]any{"status": "already_on"}, func() string {
			return "Attention: already on"
		})
	}
	cfg.Enabled = true
	if err := cfg.Validate(); err != nil {
		return failHint("attn_invalid_config", "attention is not fully configured",
			"run `oberth attn config` to supply the endpoint and model")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return failHint("attn_no_key", "no key is stored",
			"run `oberth attn config --key-stdin` (or --key-env) to store one")
	}
	path := paths.AttnConfigPath()
	if err := model.WriteSettings(path, cfg); err != nil {
		return err
	}
	return attnPrint(map[string]any{"status": "enabled", "path": path}, func() string {
		return "Attention: on\n  run `oberth attn test` to verify it answers"
	})
}

func runAttnOff(cmd *cobra.Command, _ []string) error {
	cfg, present, err := model.LoadLocalConfig()
	if err != nil {
		cfg = model.LocalConfig{}
	}
	if !present && cfg.APIKey == "" && !attnOffForgetFlag {
		return attnPrint(map[string]any{"status": "already_off"}, func() string {
			return "Attention: already off"
		})
	}
	path := paths.AttnConfigPath()
	if err := model.DisableSettings(path, cfg, attnOffForgetFlag); err != nil {
		return err
	}
	status := "disabled"
	if attnOffForgetFlag {
		status = "disabled_and_forgotten"
	}
	return attnPrint(map[string]any{"status": status, "path": path, "key_kept": !attnOffForgetFlag},
		func() string {
			if attnOffForgetFlag {
				return "Attention: off, stored key removed"
			}
			return "Attention: off, stored key kept (use --forget to remove it)"
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
			return fmt.Sprintf("Attention: the endpoint answered (%s, %dms)", probe.Model, probe.Latency.Milliseconds())
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

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
