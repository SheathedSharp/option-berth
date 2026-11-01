package main

// The adapter's single consent resolution. Every question the old default*
// helpers answered separately — enabled, provider, endpoint, model, key,
// timeout, reason — is answered once, by jev.Resolve, from the settings file the
// client writes, the environment, and the legacy key. Flags override the fields
// they name and nothing else.

import (
	"flag"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/jev"
)

// resolveConsent loads the settings and the legacy key, then applies the flags
// that were actually given. explicitKey is the parsed --api-key value, and
// keyProvided distinguishes an explicit empty value (which turns the key off
// for a harness) from an unset flag.
func resolveConsent(explicitKey string, keyProvided bool) jev.Resolved {
	input := jev.ResolveInputFromEnvironment()
	input.Settings, input.SettingsPresent, input.SettingsErr = jev.LoadLocalJevConfig()
	legacy, legacyErr := jev.ReadLegacyJevKey()
	input.LegacyKey, input.LegacyErr = legacy, legacyErr
	input.LegacyFound = strings.TrimSpace(legacy) != ""

	resolved := jev.Resolve(input)
	// --endpoint, --model and --timeout name a field to override. They do not
	// grant consent: a flag on a disabled configuration still ends in the fixed
	// "disabled" reason.
	if flagSet("endpoint") {
		resolved.Endpoint = strings.TrimSpace(flagValue("endpoint"))
	}
	if flagSet("model") {
		resolved.Model = strings.TrimSpace(flagValue("model"))
	}
	if flagSet("timeout") {
		if timeout := flagDuration("timeout"); timeout > 0 {
			resolved.Timeout = timeout
		}
	}
	if keyProvided {
		// An explicit value wins verbatim, including empty. This is the test
		// harness path and stays unchanged.
		resolved.Key = strings.TrimSpace(explicitKey)
		if resolved.Key != "" {
			resolved.KeySource = jev.SourceFlag
		} else {
			resolved.KeySource = jev.SourceNone
		}
	}
	return resolved
}

// flagSet, flagValue and flagDuration read the parsed flags through the default
// flag set so the resolution stays one function instead of threading five more
// parameters through every call site.
func flagSet(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func flagValue(name string) string {
	f := flag.Lookup(name)
	if f == nil {
		return ""
	}
	return f.Value.String()
}

func flagDuration(name string) time.Duration {
	f := flag.Lookup(name)
	if f == nil {
		return 0
	}
	parsed, err := time.ParseDuration(f.Value.String())
	if err != nil {
		// flag.Value already rejected a malformed duration at parse time; a
		// value that still fails here is not a duration at all.
		return 0
	}
	return parsed
}

// flagString and flagBool keep the resolution independent of the *string the
// flag package returned, so consent.go does not need every flag passed in.
func flagString(name string) string { return flagValue(name) }
