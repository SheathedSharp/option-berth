package model

import (
	"strings"
	"testing"
)

func TestEndpointValidationAcceptsHTTPSAndLoopbackHTTP(t *testing.T) {
	for _, endpoint := range []string{
		"https://assess.example/decisions",
		"http://localhost:8080/decisions",
		"http://127.0.0.1:8080/decisions",
		"http://[::1]:8080/decisions",
	} {
		if err := validateEndpoint(endpoint); err != nil {
			t.Errorf("%s: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{
		"http://assess.example/decisions",
		"ftp://assess.example/decisions",
		"/relative",
		"",
	} {
		if err := validateEndpoint(endpoint); err == nil {
			t.Errorf("%s was accepted", endpoint)
		}
	}
}

func TestValidateRejectsUnknownPreset(t *testing.T) {
	cfg := LocalConfig{
		Enabled: true, Preset: "acme",
		Endpoint: "https://assess.example/decisions", Model: "test/model", APIKey: "k",
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "preset") {
		t.Fatalf("err = %v, want an unknown-preset error", err)
	}
}

func TestValidateRequiresEndpointAndModelWhenEnabled(t *testing.T) {
	for _, cfg := range []LocalConfig{
		{Enabled: true, Model: "test/model", APIKey: "k"},
		{Enabled: true, Endpoint: "https://assess.example/decisions", APIKey: "k"},
	} {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("incomplete config was accepted: %+v", cfg)
		}
	}
	// A disabled file is allowed to be partial: it describes a decision, not a
	// working configuration.
	disabled := LocalConfig{Enabled: false}
	if err := disabled.Validate(); err != nil {
		t.Fatalf("disabled config rejected: %v", err)
	}
}

func TestPresetDefaultsFillOnlyWhatTheSetupLeftEmpty(t *testing.T) {
	endpoint, modelName, ok := PresetDefaults(" OpenRouter ")
	if !ok || endpoint == "" || modelName == "" {
		t.Fatalf("preset defaults = %q %q %v", endpoint, modelName, ok)
	}
	if _, _, ok := PresetDefaults("acme"); ok {
		t.Fatal("an unknown preset resolved defaults")
	}

	filled := (EnableOptions{Preset: "openrouter"}).SettingsFrom(LocalConfig{})
	if filled.Endpoint != endpoint || filled.Model != modelName || filled.Preset != "openrouter" {
		t.Fatalf("preset fill = %+v", filled)
	}

	overridden := (EnableOptions{Preset: "openrouter", Endpoint: "https://custom.example/d", Model: "custom/model"}).
		SettingsFrom(LocalConfig{})
	if overridden.Endpoint != "https://custom.example/d" || overridden.Model != "custom/model" {
		t.Fatalf("explicit fields did not win: %+v", overridden)
	}

	custom := (EnableOptions{Endpoint: "https://custom.example/d", Model: "custom/model"}).
		SettingsFrom(LocalConfig{Preset: "openrouter", Endpoint: "https://old.example/d", Model: "old/model"})
	if custom.Preset != "" {
		t.Fatalf("a custom endpoint kept the stale preset label: %+v", custom)
	}
}
