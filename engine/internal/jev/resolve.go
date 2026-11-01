package jev

// Consent resolution. One function decides every question the adapter used to
// answer in scattered default* helpers: is Jev enabled, which provider and
// endpoint, which key, and — when it cannot run — which fixed reason a caller
// may show.
//
// The rule this file encodes: an environment variable is a secret, not consent.
// A key sitting in a shell profile is not the person saying "send this event to
// a provider"; only `enabled: true` in the owner-only jev.json, or a single
// one-shot BERTH_JEV=1 for that process, is. The old decide.key file was never
// consent either, so it can supply a key but can never switch Jev on.

import (
	"os"
	"strings"
	"time"
)

// Source says where an effective setting came from. It is deliberately part of
// the resolved value: `oberth jev status` reports it, and the migration hint for
// environment-only users depends on telling the two apart.
type Source string

const (
	// SourceNone means nothing configured this setting.
	SourceNone Source = ""
	// SourceSettings is the owner-only jev.json the client's settings page writes.
	SourceSettings Source = "settings"
	// SourceEnvironment is a provider key or endpoint from the environment.
	SourceEnvironment Source = "environment"
	// SourceOneShot is BERTH_JEV=1, consent for this process only.
	SourceOneShot Source = "one-shot"
	// SourceLegacyKey is the pre-settings decide.key file. It can carry a key,
	// never consent.
	SourceLegacyKey Source = "legacy_key"
	// SourceFlag is an explicit command-line flag.
	SourceFlag Source = "flag"
)

// Provider names, matching the jev.json provider field.
const (
	ProviderOpenRouter = "openrouter"
	ProviderTypeSafe   = "typesafe"
)

// ResolveInput is everything the environment can contribute. Tests set it
// directly; the command fills it from the process environment. Keeping it a
// value instead of reading os.Getenv inside resolution is what makes the
// consent matrix table-testable.
type ResolveInput struct {
	// OneShot is BERTH_JEV. "1" (or "true") grants consent for this process.
	OneShot string
	// Settings is the loaded jev.json; Present distinguishes "absent" from
	// "present and enabled: false", which are different answers.
	Settings        LocalJevConfig
	SettingsPresent bool
	// SettingsErr is a malformed or unsafe settings file. It is reported as
	// invalid configuration, never silently ignored.
	SettingsErr error
	// Environment provider keys. A non-empty key is what selects a provider.
	OpenRouterKey string
	OpenRouterURL string
	OpenRouterMod string
	TypeSafeKey   string
	TypeSafeURL   string
	TypeSafeMod   string
	// LegacyKey is decide.key, read only to supply a secret.
	LegacyKey   string
	LegacyErr   error
	LegacyFound bool
}

// ResolveInputFromEnvironment reads the process environment. It never reads a
// file: the caller loads settings and the legacy key so an isolated run can
// point them at its own paths.
func ResolveInputFromEnvironment() ResolveInput {
	return ResolveInput{
		OneShot:       strings.TrimSpace(os.Getenv("BERTH_JEV")),
		OpenRouterKey: strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")),
		OpenRouterURL: strings.TrimSpace(os.Getenv("OPENROUTER_ENDPOINT")),
		OpenRouterMod: strings.TrimSpace(os.Getenv("OPENROUTER_MODEL")),
		TypeSafeKey:   strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY")),
		TypeSafeURL:   strings.TrimSpace(os.Getenv("TYPESAFE_ENDPOINT")),
		TypeSafeMod:   strings.TrimSpace(os.Getenv("TYPESAFE_MODEL")),
	}
}

// Resolved is the one answer every caller uses. Enabled is the consent bit;
// nothing else in this struct grants it.
type Resolved struct {
	// Enabled reports whether this process may contact a provider at all.
	Enabled bool
	// ConsentSource says which declaration enabled it, so status can explain
	// why Jev is on without exposing a key.
	ConsentSource Source
	// Provider is the selected provider, empty when nothing was configured.
	Provider string
	Endpoint string
	Model    string
	// Key is the secret to send. It is never logged, copied into telemetry or
	// written into an attention artifact.
	Key string
	// KeySource says where the secret came from, so `oberth jev status` can
	// report "key from environment" without printing the key.
	KeySource Source
	Timeout   time.Duration
	// Reason is the fixed reason code when Enabled is false, and empty when it
	// is true. See the Reason* constants.
	Reason string
	// MigrationHint asks the caller to print the one-line notice that consent
	// now has to be explicit. It is set for a user who has a usable key from
	// the environment or decide.key but has never enabled Jev in settings.
	MigrationHint bool
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

// oneShotConsent reports whether BERTH_JEV grants consent for this process.
// Only an explicit affirmative counts; "0", "false" and any other value do not.
func oneShotConsent(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// Resolve applies the consent rule and picks one provider. A malformed
// settings file is a closed door with a reason, not a fall-through to
// whatever the environment happens to hold.
func Resolve(in ResolveInput) Resolved {
	resolved := Resolved{Timeout: DefaultJevTimeout}

	settingsUsable := in.SettingsPresent && in.SettingsErr == nil
	if in.SettingsErr != nil {
		resolved.Provider = ProviderTypeSafe
		resolved.Endpoint = firstNonEmpty(in.TypeSafeURL, DefaultJevEndpoint)
		resolved.Model = firstNonEmpty(in.TypeSafeMod, DefaultJevModel)
		resolved.Reason = ReasonInvalidConfig
		return resolved
	}

	// Consent is decided first and on its own. Everything below this block is
	// about which provider to use, never about whether it is allowed to run.
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

	provider := ""
	if settingsUsable {
		provider = strings.ToLower(strings.TrimSpace(in.Settings.Provider))
	}
	// The environment may name the provider by supplying its key. It does not
	// grant consent, but once consent exists the key says which provider the
	// secret belongs to.
	if provider == "" {
		switch {
		case in.OpenRouterKey != "":
			provider = ProviderOpenRouter
		case in.TypeSafeKey != "":
			provider = ProviderTypeSafe
		}
	}
	if provider == "" && settingsUsable {
		provider = strings.ToLower(strings.TrimSpace(in.Settings.Provider))
	}
	resolved.Provider = provider
	if resolved.Provider == "" {
		resolved.Provider = ProviderTypeSafe
	}

	resolved.Endpoint, resolved.Model = providerEndpointAndModel(resolved.Provider, in, settingsUsable)
	if settingsUsable && in.Settings.Timeout() > 0 {
		resolved.Timeout = in.Settings.Timeout()
	}

	// The key search is independent of consent, so a migration hint can say
	// "there is a usable key here, you only have to enable Jev". A key found
	// without consent is then dropped from the value: a disabled Resolved must
	// not be able to leak a secret into a log line or a JSON document, and the
	// only thing it was needed for is the boolean below.
	key, keySource := pickKey(resolved.Provider, in, settingsUsable)

	if resolved.Enabled {
		resolved.Key, resolved.KeySource = key, keySource
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
		// A key from the environment or decide.key with no declared consent is
		// the breaking case: those users relied on the old implicit enable. Tell
		// them once, from the shell, instead of failing silently.
		resolved.MigrationHint = true
	default:
		resolved.Reason = ReasonNoKey
	}
	return resolved
}

func providerEndpointAndModel(provider string, in ResolveInput, settingsUsable bool) (string, string) {
	if provider == ProviderOpenRouter {
		endpoint := DefaultOpenRouterEndpoint
		model := DefaultOpenRouterModel
		if settingsUsable && strings.EqualFold(in.Settings.Provider, ProviderOpenRouter) {
			endpoint = firstNonEmpty(in.Settings.Endpoint, endpoint)
			model = firstNonEmpty(in.Settings.Model, model)
		}
		return firstNonEmpty(in.OpenRouterURL, endpoint), firstNonEmpty(in.OpenRouterMod, model)
	}
	endpoint := DefaultJevEndpoint
	model := DefaultJevModel
	if settingsUsable && !strings.EqualFold(in.Settings.Provider, ProviderOpenRouter) {
		endpoint = firstNonEmpty(in.Settings.Endpoint, endpoint)
		model = firstNonEmpty(in.Settings.Model, model)
	}
	return firstNonEmpty(in.TypeSafeURL, endpoint), firstNonEmpty(in.TypeSafeMod, model)
}

// pickKey answers where the secret comes from. The environment wins over the
// stored key, which is the order the adapter already documented and what CI and
// one-off isolated runs rely on; decide.key comes last because it predates the
// settings page and can never switch Jev on by itself.
func pickKey(provider string, in ResolveInput, settingsUsable bool) (string, Source) {
	if provider == ProviderTypeSafe && in.TypeSafeKey != "" {
		return in.TypeSafeKey, SourceEnvironment
	}
	if provider == ProviderOpenRouter && in.OpenRouterKey != "" {
		return in.OpenRouterKey, SourceEnvironment
	}
	switch {
	case in.TypeSafeKey != "":
		return in.TypeSafeKey, SourceEnvironment
	case in.OpenRouterKey != "":
		return in.OpenRouterKey, SourceEnvironment
	case settingsUsable && strings.TrimSpace(in.Settings.APIKey) != "":
		return strings.TrimSpace(in.Settings.APIKey), SourceSettings
	case in.LegacyFound && strings.TrimSpace(in.LegacyKey) != "":
		return strings.TrimSpace(in.LegacyKey), SourceLegacyKey
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
	if strings.TrimSpace(r.Key) == "" {
		return Client{}, ErrUnavailable
	}
	return Client{Endpoint: r.Endpoint, Model: r.Model, APIKey: r.Key, Timeout: r.Timeout}, nil
}

// RedactedState is what `oberth jev status` may print: everything about the
// configuration except the secret itself.
func (r Resolved) RedactedState() map[string]any {
	state := map[string]any{
		"enabled":        r.Enabled,
		"provider":       r.Provider,
		"endpoint":       r.Endpoint,
		"model":          r.Model,
		"timeout_ms":     r.Timeout.Milliseconds(),
		"key_source":     string(r.KeySource),
		"consent_source": string(r.ConsentSource),
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
