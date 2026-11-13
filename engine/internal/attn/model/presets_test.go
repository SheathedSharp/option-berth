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

// The catalog is what the interactive setup draws. A vendor whose default model
// is not in its own list would preselect nothing, and a duplicate or empty row
// would be a choice that cannot be stored.
func TestVendorCatalogIsInternallyConsistent(t *testing.T) {
	seen := make(map[string]bool)
	for _, vendor := range Vendors() {
		if vendor.Name == "" || vendor.Label == "" || vendor.Endpoint == "" || vendor.Default == "" {
			t.Errorf("incomplete vendor row: %+v", vendor)
			continue
		}
		if seen[vendor.Name] {
			t.Errorf("duplicate vendor %q", vendor.Name)
		}
		seen[vendor.Name] = true
		if _, modelName, ok := PresetDefaults(vendor.Name); !ok || modelName != vendor.Default {
			t.Errorf("preset %q does not resolve its default model", vendor.Name)
		}
		hasDefault := false
		modelIDs := make(map[string]bool, len(vendor.Models))
		for _, candidate := range vendor.Models {
			if candidate.ID == "" || candidate.Label == "" {
				t.Errorf("%s has an incomplete model row: %+v", vendor.Name, candidate)
				continue
			}
			if modelIDs[candidate.ID] {
				t.Errorf("%s lists %q twice", vendor.Name, candidate.ID)
			}
			modelIDs[candidate.ID] = true
			if candidate.ID == vendor.Default {
				hasDefault = true
			}
		}
		if !hasDefault {
			t.Errorf("%s default %q is not one of its models", vendor.Name, vendor.Default)
		}
	}
	if len(seen) < 2 {
		t.Fatalf("vendors = %v, want the gateway and the direct vendor", seen)
	}
}

// The gateway exists to reach more than one maker. Pinning that shape keeps a
// future edit from quietly collapsing the list back to a single model.
func TestOpenRouterCarriesMoreThanOneMaker(t *testing.T) {
	vendor, ok := VendorByName("openrouter")
	if !ok {
		t.Fatal("the openrouter preset is missing")
	}
	makers := make(map[string]bool)
	for _, candidate := range vendor.Models {
		maker, _, found := strings.Cut(candidate.ID, "/")
		if !found {
			t.Errorf("model %q has no maker prefix", candidate.ID)
			continue
		}
		makers[maker] = true
	}
	if len(makers) < 3 {
		t.Fatalf("openrouter models cover %d makers, want at least three", len(makers))
	}
	if vendor.Default != "typesafe/jev-1.14" {
		t.Fatalf("default model = %q, want the current Jev", vendor.Default)
	}
}
