package model

// Presets are setup conveniences, not provider restrictions: they fill the
// endpoint and model fields when a person runs `oberth attn config --preset
// NAME`. Nothing at runtime consults a preset, and any endpoint that speaks the
// structured decisions protocol can be configured by giving endpoint and model
// directly.

import "strings"

// preset is one named pair of endpoint and model defaults.
type preset struct {
	Name     string
	Endpoint string
	Model    string
}

var presetCatalog = []preset{
	{
		Name:     "openrouter",
		Endpoint: "https://openrouter.ai/api/alpha/decisions",
		Model:    "typesafe/jev-1.13",
	},
	{
		Name:     "typesafe",
		Endpoint: "https://api.typesafe.ai/v1/systemone",
		Model:    "jev-latest",
	},
}

// PresetDefaults answers the endpoint and model a named preset fills in.
func PresetDefaults(name string) (endpoint, model string, ok bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, preset := range presetCatalog {
		if preset.Name == name {
			return preset.Endpoint, preset.Model, true
		}
	}
	return "", "", false
}

// PresetNames lists the known names for help text and error hints.
func PresetNames() string {
	names := make([]string, 0, len(presetCatalog))
	for _, preset := range presetCatalog {
		names = append(names, preset.Name)
	}
	return strings.Join(names, ", ")
}
