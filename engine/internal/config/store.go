package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// The runtime service's `config.get`, `config.set` and `config.path` methods operate
// on a plain map rather than the Config struct, so a key this build does not
// know survives a round trip: a newer client writing `remote.hosts` must not
// lose it to an older runtime.service.
//
// Comments do not survive re-marshalling. The copy at <path>.bak is what makes
// that recoverable.

// BackupSuffix is appended to the config path for the pre-write copy.
const BackupSuffix = ".bak"

// Map reads the config file into a JSON-safe map. A missing file is an empty
// map, not an error: never having written one is a normal state.
func Map() (map[string]any, error) {
	path := Path()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var decoded any
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	root, ok := normalize(decoded).(map[string]any)
	if !ok || root == nil {
		return map[string]any{}, nil
	}
	return root, nil
}

// Apply merges patch into the config file and writes it back. A null removes
// the key; nested maps merge key by key, so setting `runtime_service.log_level` leaves
// `runtime_service.idle_timeout` alone. The written config is returned.
//
// The merged result must parse and validate as a Config. When it does not,
// nothing is written and the validation messages become the error.
func Apply(patch map[string]any) (map[string]any, error) {
	existing, err := Map()
	if err != nil {
		return nil, err
	}
	merged := merge(existing, patch)
	if problem := validateMap(merged); problem != nil {
		return nil, problem
	}
	if err := Save(merged); err != nil {
		return nil, err
	}
	return merged, nil
}

// Save writes m to the config path, copying whatever is there to <path>.bak
// first. The new file is written to a temporary file and renamed, so a crash
// mid-write cannot leave a half-written config behind.
func Save(m map[string]any) error {
	path := Path()
	mode, err := prepareSave(path)
	if err != nil {
		return err
	}
	body, err := yaml.Marshal(denormalize(m))
	if err != nil {
		return fmt.Errorf("encoding the config: %w", err)
	}
	return writeAtomically(path, body, mode)
}

// prepareSave creates the directory, remembers the current file mode and
// keeps the pre-write copy. A file that does not exist yet uses 0644.
func prepareSave(path string) (os.FileMode, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0o644, nil
	}
	mode := info.Mode().Perm()
	if err := backup(path, mode); err != nil {
		return 0, err
	}
	return mode, nil
}

func writeAtomically(path string, body []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return writeError(path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return writeError(path, err)
	}
	if err := tmp.Close(); err != nil {
		return writeError(path, err)
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return writeError(path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}

func writeError(path string, err error) error {
	return fmt.Errorf("writing %s: %w", path, err)
}

func backup(path string, mode os.FileMode) error {
	previous, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	kept := path + BackupSuffix
	if err := os.WriteFile(kept, previous, mode); err != nil {
		return fmt.Errorf("writing %s: %w", kept, err)
	}
	return nil
}

// BackupPath is where Save parks the previous config.
func BackupPath() string { return Path() + BackupSuffix }

// merge deep-merges patch into base and returns a new map. A nil deletes the
// key, two maps merge, and anything else replaces.
func merge(base, patch map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(patch))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range patch {
		if value == nil {
			delete(out, key)
			continue
		}
		incoming, incomingIsMap := normalize(value).(map[string]any)
		existing, existingIsMap := out[key].(map[string]any)
		if incomingIsMap && existingIsMap {
			out[key] = merge(existing, incoming)
			continue
		}
		out[key] = normalize(value)
	}
	return out
}

// validateMap decodes a merged config back into Config and rejects one whose
// settings this build would repair.
func validateMap(m map[string]any) error {
	if _, retired := m["daemon"]; retired {
		return fmt.Errorf("config key daemon was removed; use runtime_service")
	}
	body, err := yaml.Marshal(denormalize(m))
	if err != nil {
		return fmt.Errorf("encoding the config: %w", err)
	}
	decoded := &Config{}
	if err := yaml.Unmarshal(body, decoded); err != nil {
		return fmt.Errorf("the merged config is not valid: %w", err)
	}
	if problems := validate(decoded); len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// normalize converts YAML's containers into JSON-safe ones: map keys become
// strings and nested containers are converted recursively.
func normalize(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[key] = normalize(value)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[fmt.Sprint(key)] = normalize(value)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index := range typed {
			out[index] = normalize(typed[index])
		}
		return out
	default:
		return v
	}
}

// denormalize undoes normalize where YAML cares: `services` is keyed by port
// number, and a quoted "9000" would no longer decode as an int key.
func denormalize(v any) any {
	mapping, ok := v.(map[string]any)
	if !ok {
		list, isList := v.([]any)
		if !isList {
			return v
		}
		out := make([]any, len(list))
		for index := range list {
			out[index] = denormalize(list[index])
		}
		return out
	}

	allNumeric := len(mapping) > 0
	for key := range mapping {
		if _, err := strconv.Atoi(key); err != nil {
			allNumeric = false
			break
		}
	}
	if !allNumeric {
		out := make(map[string]any, len(mapping))
		for key, value := range mapping {
			out[key] = denormalize(value)
		}
		return out
	}
	out := make(map[int]any, len(mapping))
	for key, value := range mapping {
		number, _ := strconv.Atoi(key)
		out[number] = denormalize(value)
	}
	return out
}
