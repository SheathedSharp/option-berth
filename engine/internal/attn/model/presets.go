package model

// Presets are setup conveniences, not provider restrictions: they give the
// interactive setup a short list of endpoints and the decision models each one
// serves. `oberth attn config --preset NAME` still fills the endpoint and that
// vendor's default model. Nothing at runtime consults a preset, and any
// endpoint that speaks the structured decisions protocol can be configured by
// giving endpoint and model directly.
//
// The model list is a short, curated set rather than a mirror of every
// catalogue: it exists so a person can recognise the model they already pay
// for. A model a provider retires is a wrong row in a list, not a broken
// promise — the configuration probe is what proves a choice answers before
// anything is written.

import "strings"

// VendorModel is one decision model a vendor serves.
type VendorModel struct {
	// ID is the model field the provider expects.
	ID string
	// Label is the short name a person recognises.
	Label string
	// Note says what the model is, when the name alone is not enough.
	Note string
}

// Vendor is one endpoint and the models it serves. Name is also the value
// `--preset` accepts, and the label stored in attn.json.
type Vendor struct {
	Name     string
	Label    string
	Summary  string
	Endpoint string
	// Default is the model a bare `--preset NAME` fills in.
	Default string
	Models  []VendorModel
}

// vendorCatalog is the ordered list the interactive setup shows. Order is
// deliberate: the gateway first because one key reaches every model below it,
// then the vendor whose model this adapter was built against. A custom endpoint
// is appended by the setup, not listed here: it is not a preset.
var vendorCatalog = []Vendor{
	{
		Name:     "openrouter",
		Label:    "OpenRouter",
		Summary:  "one key, many decision models",
		Endpoint: "https://openrouter.ai/api/alpha/decisions",
		Default:  "typesafe/jev-1.14",
		Models: []VendorModel{
			{ID: "typesafe/jev-1.14", Label: "TypeSafe Jev 1.14", Note: "the model this adapter was built against"},
			{ID: "openai/gpt-6-luna-decisions", Label: "OpenAI GPT-6 Luna Decisions"},
			{ID: "google/gemini-decision-1", Label: "Google Gemini Decision 1"},
			{ID: "microsoft/microsoft-decision-1", Label: "Microsoft Decision 1"},
			{ID: "perplexity/pplx-decider-v1.3", Label: "Perplexity Decider v1.3"},
			{ID: "cloudflare/clef", Label: "Cloudflare Clef", Note: "open-source decision model"},
			{ID: "liquid/d1.1", Label: "LiquidAI d1.1"},
			{ID: "upstage/solar-decide", Label: "Upstage Solar Decide"},
			{ID: "morph/reflex", Label: "Morph Reflex"},
		},
	},
	{
		Name:     "typesafe",
		Label:    "TypeSafe",
		Summary:  "Jev directly from TypeSafe",
		Endpoint: "https://api.typesafe.ai/v1/systemone",
		Default:  "jev-latest",
		Models: []VendorModel{
			{ID: "jev-latest", Label: "Jev latest"},
			{ID: "jev-preview", Label: "Jev preview", Note: "early builds of the next version"},
		},
	},
}

// Vendors returns the known vendor presets in display order.
func Vendors() []Vendor {
	vendors := make([]Vendor, len(vendorCatalog))
	copy(vendors, vendorCatalog)
	return vendors
}

// VendorByName answers one preset by its command-line name.
func VendorByName(name string) (Vendor, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, vendor := range vendorCatalog {
		if vendor.Name == name {
			return vendor, true
		}
	}
	return Vendor{}, false
}

// PresetDefaults answers the endpoint and model a named preset fills in.
func PresetDefaults(name string) (endpoint, model string, ok bool) {
	vendor, ok := VendorByName(name)
	if !ok {
		return "", "", false
	}
	return vendor.Endpoint, vendor.Default, true
}

// PresetNames lists the known names for help text and error hints.
func PresetNames() string {
	names := make([]string, 0, len(vendorCatalog))
	for _, vendor := range vendorCatalog {
		names = append(names, vendor.Name)
	}
	return strings.Join(names, ", ")
}
