package cmd

import (
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
)

// The guided setup exists to give the choice back to the person: every list
// ends with a row that leaves the catalog and asks for the field directly.
func TestProviderChoicesEndWithACustomEndpoint(t *testing.T) {
	choices, initial := attnProviderChoices(model.LocalConfig{})
	if initial != 0 {
		t.Fatalf("fresh setup starts on row %d, want the gateway", initial)
	}
	if choices[0].Value != "openrouter" {
		t.Fatalf("first row = %q, want openrouter", choices[0].Value)
	}
	if want := len(model.Vendors()) + 1; len(choices) != want {
		t.Fatalf("choices = %d, want %d (every vendor plus custom)", len(choices), want)
	}
	if last := choices[len(choices)-1]; last.Value != attnCustomEndpoint || last.Label == "" {
		t.Fatalf("last row = %+v, want a custom endpoint", last)
	}
}

func TestProviderChoicesPreselectTheStoredVendor(t *testing.T) {
	choices, initial := attnProviderChoices(model.LocalConfig{
		Preset: "typesafe", Endpoint: "https://api.typesafe.ai/v1/systemone", Model: "jev-latest",
	})
	if choices[initial].Value != "typesafe" {
		t.Fatalf("highlighted %q, want the stored vendor", choices[initial].Value)
	}
}

func TestProviderChoicesLandOnCustomForAnUnknownEndpoint(t *testing.T) {
	choices, initial := attnProviderChoices(model.LocalConfig{
		Endpoint: "https://assess.example/decisions", Model: "team/model",
	})
	if choices[initial].Value != attnCustomEndpoint {
		t.Fatalf("highlighted %q, want the custom row that keeps the endpoint", choices[initial].Value)
	}
}

func TestModelChoicesPreselectAKnownModelAndOfferCustom(t *testing.T) {
	vendor, ok := model.VendorByName("openrouter")
	if !ok {
		t.Fatal("openrouter is missing")
	}
	choices, initial := attnModelChoices(vendor, model.LocalConfig{Model: vendor.Default})
	if choices[initial].Value != vendor.Default {
		t.Fatalf("highlighted %q, want the stored model", choices[initial].Value)
	}
	if last := choices[len(choices)-1]; last.Value != attnCustomModel {
		t.Fatalf("last row = %+v, want a custom model", last)
	}
}

// Reopening the setup must not quietly replace a model this vendor does not
// list: the person's stored choice is a working configuration until they say
// otherwise.
func TestModelChoicesPutAnUnlistedStoredModelOnCustom(t *testing.T) {
	vendor, ok := model.VendorByName("typesafe")
	if !ok {
		t.Fatal("typesafe is missing")
	}
	choices, initial := attnModelChoices(vendor, model.LocalConfig{Model: "acme/private"})
	if choices[initial].Value != attnCustomModel {
		t.Fatalf("highlighted %q, want the custom row", choices[initial].Value)
	}
}

// The non-interactive listing is the only discovery a pipe or a Windows build
// gets, so it has to name every endpoint and model the picker would show.
func TestProviderListMentionsEveryVendorAndModel(t *testing.T) {
	list := attnProviderList()
	for _, vendor := range model.Vendors() {
		for _, want := range []string{vendor.Name, vendor.Endpoint, vendor.Summary} {
			if !strings.Contains(list, want) {
				t.Errorf("provider list is missing %q:\n%s", want, list)
			}
		}
		for _, candidate := range vendor.Models {
			if !strings.Contains(list, candidate.ID) {
				t.Errorf("provider list is missing model %q:\n%s", candidate.ID, list)
			}
		}
	}
	if !strings.Contains(list, "--preset") {
		t.Errorf("provider list does not name the flag path:\n%s", list)
	}
}

// A flag or --json means the caller already decided: the guided form must not
// start even on a terminal.
func TestWizardStaysOutOfTheScriptedPaths(t *testing.T) {
	t.Cleanup(func() {
		attnJSONFlag = false
		attnConfigPreset = ""
	})
	attnJSONFlag = true
	if attnConfigWizardApplies() {
		t.Fatal("--json started the wizard")
	}
	attnJSONFlag = false
	attnConfigPreset = "openrouter"
	if attnConfigWizardApplies() {
		t.Fatal("--preset started the wizard")
	}
}

func TestReusableStoredKeyIsolatesProvidersAndLoopback(t *testing.T) {
	openai := model.LocalConfig{
		Preset:   "openai",
		Endpoint: "https://api.openai.com/v1/chat/completions",
		Model:    "gpt-4.1-mini",
		APIKey:   "sk-openai-secret",
	}
	ollama := model.LocalConfig{
		Preset:   "ollama",
		Endpoint: "http://127.0.0.1:11434/v1/chat/completions",
		Model:    "qwen2.5:7b",
		APIKey:   model.LoopbackKeyPlaceholder,
	}
	anthropic := model.LocalConfig{
		Preset:   "anthropic",
		Endpoint: "https://api.anthropic.com/v1/messages",
		Model:    "claude-haiku-4-5",
	}
	sameHostNewModel := model.LocalConfig{
		Preset:   "openai",
		Endpoint: "https://api.openai.com/v1/chat/completions",
		Model:    "gpt-4.1-nano",
	}

	if got := reusableStoredKey(openai, ollama); got != "" {
		t.Fatalf("openai -> ollama leaked stored key %q", got)
	}
	if got := reusableStoredKey(ollama, openai); got != "" {
		t.Fatalf("ollama -> openai reused loopback placeholder %q", got)
	}
	if got := reusableStoredKey(openai, anthropic); got != "" {
		t.Fatalf("openai -> anthropic leaked stored key %q", got)
	}
	if got := reusableStoredKey(openai, sameHostNewModel); got != "sk-openai-secret" {
		t.Fatalf("same host model update = %q, want stored key preserved", got)
	}
}

func TestProtocolChoicesInferCustomEndpointShape(t *testing.T) {
	cases := []struct {
		endpoint string
		stored   string
		want     string
	}{
		{"https://api.deepseek.com/v1/chat/completions", "", string(model.ProtocolOpenAIChat)},
		{"https://proxy.example/v1/messages", "", string(model.ProtocolAnthropic)},
		{"https://openrouter.ai/api/alpha/decisions", "", string(model.ProtocolDecisions)},
		{"https://api.deepseek.com/v1/chat/completions", string(model.ProtocolAnthropic), string(model.ProtocolAnthropic)},
	}
	for _, tc := range cases {
		choices, initial := attnProtocolChoices(tc.endpoint, tc.stored)
		if got := choices[initial].Value; got != tc.want {
			t.Errorf("attnProtocolChoices(%q, %q) = %q, want %q", tc.endpoint, tc.stored, got, tc.want)
		}
	}
}
