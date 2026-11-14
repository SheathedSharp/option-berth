package config

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
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
	return checkRuntimeServiceConfigNames(data)
}

// checkRuntimeServiceConfigNames is also used by config.set validation; one
// parser owns the retired-key and safety-guard policy for both entry points.
func checkRuntimeServiceConfigNames(data []byte) error {
	var keys map[string]yaml.Node
	if yaml.Unmarshal(data, &keys) != nil {
		return fmt.Errorf("runtime_service configuration must be valid YAML before an operation; safety guards cannot fall back to defaults")
	}
	if _, retired := keys["daemon"]; retired {
		return fmt.Errorf("config key daemon was removed; rename it to runtime_service after explicitly stopping any previous host; configuration was not changed")
	}
	node, present := keys["runtime_service"]
	if !present {
		return nil
	}
	var fields map[string]yaml.Node
	if node.Decode(&fields) != nil {
		return fmt.Errorf("runtime_service must be a configuration mapping")
	}
	guards, present := fields["previous_service_lock_dirs"]
	if !present {
		return nil
	}
	var dirs []string
	if guards.Decode(&dirs) != nil {
		return fmt.Errorf("runtime_service.previous_service_lock_dirs requires a list of absolute directories")
	}
	for _, dir := range dirs {
		if dir == "" || !filepath.IsAbs(dir) {
			return fmt.Errorf("runtime_service.previous_service_lock_dirs requires non-empty absolute directories")
		}
	}
	return nil
}
