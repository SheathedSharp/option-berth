package config

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
)

// CheckRuntimeServiceConfigNames prevents an old stanza from being silently
// ignored and replaced by defaults. It never rewrites user configuration.
func CheckRuntimeServiceConfigNames(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading runtime service configuration: %w", err)
	}
	var keys map[string]yaml.Node
	// Decoding the root mapping resolves YAML merge aliases too. Inspecting only
	// the physical root nodes would silently miss a retired key supplied by <<.
	// Existing malformed-YAML handling belongs to Load/doctor, not this rename.
	if yaml.Unmarshal(data, &keys) != nil {
		return nil
	}
	if _, retired := keys["daemon"]; retired {
		return fmt.Errorf("config key daemon was removed; rename it to runtime_service after explicitly stopping any previous host; configuration was not changed")
	}
	return nil
}
