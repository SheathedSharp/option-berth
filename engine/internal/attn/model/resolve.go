package model

// Consent resolution. One function decides every question the caller could
// otherwise answer in scattered default* helpers: is the assessment enabled,
// which endpoint and model, which key, and — when it cannot run — which fixed
// reason a caller may show.
//
// The rule this file encodes: an environment variable is a secret, not consent.
// A key sitting in a shell profile is not the person saying "send this event to
// a provider"; only `enabled: true` in the owner-only attn.json, or a single
// one-shot BERTH_ATTN=1 for that process, is.
//
// Endpoint, model and key are three independent fields. Nothing here selects a
// provider: a preset only fills defaults while the setup command writes the
// file, and the environment overrides those same fields per process.

import (
	"os"
	"strings"
	"time"
)

// Source says where an effective setting came from. It is deliberately part of
// the resolved value: `oberth attn status` reports it.
type Source string

const (
	// SourceNone means nothing configured this setting.
	SourceNone Source = ""
	// SourceSettings is the owner-only attn.json the setup command writes.
	SourceSettings Source = "settings"
	// SourceEnvironment is a key, endpoint or model from the environment.
	SourceEnvironment Source = "environment"
	// SourceOneShot is BERTH_ATTN=1, consent for this process only.
	SourceOneShot Source = "one-shot"
	// SourceFlag is an explicit command-line flag.
	SourceFlag Source = "flag"
)

// ResolveInput is everything the environment can contribute. Tests set it
// directly; the command fills it from the process environment. Keeping it a
// value instead of reading os.Getenv inside resolution is what makes the
// consent matrix table-testable.
type ResolveInput struct {
	// OneShot is BERTH_ATTN. "1" (or "true") grants consent for this process.
	OneShot string
	// Settings is the loaded attn.json; Present distinguishes "absent" from
	// "present and enabled: false", which are different answers.
	Settings        LocalConfig
	SettingsPresent bool
	// SettingsErr is a malformed or unsafe settings file. It is reported as
	// invalid configuration, never silently ignored.
	SettingsErr error
	// Endpoint, Model and Key override the stored fields for this process.
	Endpoint string
	Model    string
	Key      string
}

// ResolveInputFromEnvironment reads the process environment. It never reads a
// file, so an isolated run can point settings at its own path.
func ResolveInputFromEnvironment() ResolveInput {
	return ResolveInput{
		OneShot:  strings.TrimSpace(os.Getenv("BERTH_ATTN")),
		Endpoint: strings.TrimSpace(os.Getenv("BERTH_ATTN_ENDPOINT")),
		Model:    strings.TrimSpace(os.Getenv("BERTH_ATTN_MODEL")),
		Key:      strings.TrimSpace(os.Getenv("BERTH_ATTN_KEY")),
	}
}

// Resolved is the one answer every caller uses. Enabled is the consent bit;
// nothing else in this struct grants it.
type Resolved struct {
	// Enabled reports whether this process may contact a provider at all.
	Enabled bool
	// ConsentSource says which declaration enabled it, so status can explain
	// why the assessment is on without exposing a key.
	ConsentSource Source
	// Preset is the setup label stored with the configuration, if any. It has
	// no effect on resolution; endpoint and model are already explicit.
	Preset   string
	Endpoint string
	Model    string
	// Key is the secret to send. It is never logged, copied into telemetry or
	// written into an attention artifact.
	Key string
	// KeySource says where the secret came from, so `oberth attn status` can
	// report "key from environment" without printing the key.
	KeySource Source
	Timeout   time.Duration
	// Reason is the fixed reason code when Enabled is false, and empty when it
	// is true. See the Reason* constants.
	Reason string
}

// Fixed reason codes. Callers may branch on these; they are a closed set so a
// human-facing hint can be written for each one without parsing an error.
const (
	ReasonNoKey         = "no_key"
	ReasonDisabled      = "disabled"
	ReasonUnauthorized  = "unauthorized"
	ReasonRateLimited   = "rate_limited"
	ReasonTimeout       = "timeout"
	ReasonNetwork       = "network"
	ReasonProvider      = "provider_error"
	ReasonBadResponse   = "bad_response"
	ReasonInvalidConfig = "invalid_config"
	ReasonStale         = "stale"
)

// oneShotConsent reports whether BERTH_ATTN grants consent for this process.
// Only an explicit affirmative counts; "0", "false" and any other value do not.
func oneShotConsent(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// Resolve applies the consent rule and returns the three explicit fields. A
// malformed settings file is a closed door with a reason, not a fall-through
// to whatever the environment happens to hold.
func Resolve(in ResolveInput) Resolved {
	resolved := Resolved{Timeout: DefaultTimeout}

	settingsUsable := in.SettingsPresent && in.SettingsErr == nil
	if in.SettingsErr != nil {
		resolved.Reason = ReasonInvalidConfig
		return resolved
	}

	// Consent is decided first and on its own. Everything below is about which
	// fields to use, never about whether the call is allowed to run.
	switch {
	case settingsUsable && in.Settings.Enabled:
		resolved.Enabled, resolved.ConsentSource = true, SourceSettings
	case oneShotConsent(in.OneShot):
		resolved.Enabled, resolved.ConsentSource = true, SourceOneShot
	case settingsUsable:
		// A present settings file that is not enabled is a decision the person
		// already made. It must not be overridden by a key in the environment.
		resolved.Reason = ReasonDisabled
	default:
		// No settings file at all. Consent has never been expressed.
		resolved.Reason = ReasonDisabled
	}

	if settingsUsable {
		resolved.Preset = in.Settings.Preset
		resolved.Endpoint = in.Settings.Endpoint
		resolved.Model = in.Settings.Model
		resolved.Timeout = in.Settings.Timeout()
	}
	resolved.Endpoint = firstNonEmpty(in.Endpoint, resolved.Endpoint)
	resolved.Model = firstNonEmpty(in.Model, resolved.Model)

	// The key search is independent of consent so the closed door can be named
	// precisely. A key found without consent is then dropped from the value: a
	// disabled Resolved must not be able to leak a secret into a log line or a
	// JSON document, and the only thing it was needed for is the distinction
	// below.
	key, keySource := pickKey(in, settingsUsable)

	if resolved.Enabled {
		resolved.Key, resolved.KeySource = key, keySource
		if resolved.Endpoint == "" || resolved.Model == "" {
			resolved.Reason = ReasonInvalidConfig
			return resolved
		}
		if resolved.Key == "" {
			resolved.Reason = ReasonNoKey
		}
		return resolved
	}

	// Which door is closed? A settings file the person turned off reports
	// `disabled`; when the *only* obstacle is a missing key, say `no_key`,
	// because the next step is different: store a secret rather than opt in.
	switch {
	case settingsUsable:
		resolved.Reason = ReasonDisabled
	case key != "":
		resolved.Reason = ReasonDisabled
	default:
		resolved.Reason = ReasonNoKey
	}
	return resolved
}

// pickKey answers where the secret comes from. The environment wins over the
// stored key, which is what CI and one-off isolated runs rely on.
func pickKey(in ResolveInput, settingsUsable bool) (string, Source) {
	if in.Key != "" {
		return in.Key, SourceEnvironment
	}
	if settingsUsable && strings.TrimSpace(in.Settings.APIKey) != "" {
		return strings.TrimSpace(in.Settings.APIKey), SourceSettings
	}
	return "", SourceNone
}

// Client returns the HTTP client these settings describe. A caller that has not
// checked Enabled, or that ignores a non-empty Reason, is calling a provider
// the person did not opt into — so this refuses instead of guessing.
func (r Resolved) Client() (Client, error) {
	if !r.Enabled {
		return Client{}, ErrUnavailable
	}
	if strings.TrimSpace(r.Endpoint) == "" || strings.TrimSpace(r.Model) == "" {
		return Client{}, WithReason(ReasonInvalidConfig, ErrUnavailable)
	}
	if strings.TrimSpace(r.Key) == "" {
		return Client{}, ErrUnavailable
	}
	return Client{Endpoint: r.Endpoint, Model: r.Model, APIKey: r.Key, Timeout: r.Timeout}, nil
}

// RedactedState is what `oberth attn status` may print: everything about the
// configuration except the secret itself.
func (r Resolved) RedactedState() map[string]any {
	state := map[string]any{
		"enabled":        r.Enabled,
		"endpoint":       r.Endpoint,
		"model":          r.Model,
		"timeout_ms":     r.Timeout.Milliseconds(),
		"key_source":     string(r.KeySource),
		"consent_source": string(r.ConsentSource),
	}
	if r.Preset != "" {
		state["preset"] = r.Preset
	}
	if r.Reason != "" {
		state["reason"] = r.Reason
	}
	return state
}

// ReasonOr answers why the client was not built: the fixed consent reason when
// there is one, otherwise the failure's own code. Callers print exactly this,
// so a "disabled" run and a broken provider never look the same.
func (r Resolved) ReasonOr(err error) string {
	if r.Reason != "" {
		return r.Reason
	}
	return ReasonFor(err)
}
