package model

import "testing"

func TestResolveEnvironmentOverridesEndpointModelAndKey(t *testing.T) {
	settings := LocalConfig{
		Enabled: true, Preset: "openrouter",
		Endpoint: "https://settings.example/decisions", Model: "settings/model", APIKey: "settings-key",
	}
	got := Resolve(ResolveInput{
		Settings: settings, SettingsPresent: true,
		Endpoint: "http://127.0.0.1:8080/decisions", Model: "env/model", Key: "env-key",
	})
	if !got.Enabled || got.Preset != "openrouter" {
		t.Fatalf("resolved = %+v", got)
	}
	if got.Endpoint != "http://127.0.0.1:8080/decisions" || got.Model != "env/model" || got.Key != "env-key" {
		t.Fatalf("environment did not win: %+v", got)
	}
	if got.KeySource != SourceEnvironment {
		t.Fatalf("key source = %q", got.KeySource)
	}
}

func TestResolveEnabledWithoutEndpointOrModelIsInvalidConfig(t *testing.T) {
	for _, settings := range []LocalConfig{
		{Enabled: true, Model: "test/model", APIKey: "k"},
		{Enabled: true, Endpoint: "https://assess.example/decisions", APIKey: "k"},
	} {
		got := Resolve(ResolveInput{Settings: settings, SettingsPresent: true})
		if got.Reason != ReasonInvalidConfig {
			t.Fatalf("incomplete enabled settings = %+v, want invalid_config", got)
		}
		if _, err := got.Client(); err == nil {
			t.Fatal("an incomplete configuration produced a client")
		}
	}
}

func TestResolveOneShotStillNeedsAllThreeFields(t *testing.T) {
	got := Resolve(ResolveInput{OneShot: "1", Key: "k"})
	if got.Reason != ReasonInvalidConfig {
		t.Fatalf("one-shot without endpoint/model = %+v, want invalid_config", got)
	}
	got = Resolve(ResolveInput{
		OneShot: "1", Endpoint: "https://assess.example/decisions", Model: "test/model", Key: "k",
	})
	if !got.Enabled || got.Reason != "" || got.ConsentSource != SourceOneShot {
		t.Fatalf("complete one-shot = %+v", got)
	}
}

func TestResolveRedactedStateCarriesNoProvider(t *testing.T) {
	got := Resolve(ResolveInput{Settings: LocalConfig{
		Enabled: true, Preset: "openrouter",
		Endpoint: "https://assess.example/decisions", Model: "test/model", APIKey: "secret",
	}, SettingsPresent: true})
	state := got.RedactedState()
	if _, ok := state["provider"]; ok {
		t.Fatal("redacted state still has a provider field")
	}
	if state["preset"] != "openrouter" || state["endpoint"] != "https://assess.example/decisions" || state["model"] != "test/model" {
		t.Fatalf("redacted state = %+v", state)
	}
	for _, value := range state {
		if text, ok := value.(string); ok && text == "secret" {
			t.Fatal("redacted state leaked the key")
		}
	}
}
