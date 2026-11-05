package groups

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"gopkg.in/yaml.v3"
)

// YAML keys a service patch may write.
const (
	// Text metadata.
	FieldDescription = "description"
	FieldIcon        = "icon"
	FieldColor       = "color"
	FieldHealth      = "health"

	// The port hint.
	FieldPort = "port"
)

// keyOrder is where a key belongs in a service mapping. A key a patch adds is
// inserted at that position relative to the keys already present, so an edited
// file still reads like a hand-written one. Unknown keys sort last and never
// move.
var keyOrder = []string{"name", "prepare", "prepare_inputs", "cmd", "cwd", "sources", "port", "health", "description", "icon", "color", "depends_on"}

// ServicePatch is the editable subset of one manifest service. Three states
// matter: a key the caller did not mention stays as it is, a key with a value
// is written, and a key with null is removed. Pointers alone lose JSON's
// absent/null distinction, so UnmarshalJSON records which keys were sent.
type ServicePatch struct {
	// Text fields.
	Description *string `json:"description,omitempty" jsonschema:"nullable"`
	Icon        *string `json:"icon,omitempty" jsonschema:"nullable"`
	Color       *string `json:"color,omitempty" jsonschema:"nullable"`
	Health      *string `json:"health,omitempty" jsonschema:"nullable"`

	// The port hint.
	Port *int `json:"port,omitempty" jsonschema:"nullable"`

	// Sent names the keys the caller included, whether or not their value was
	// null. Go callers set it directly; the wire form fills it in
	// UnmarshalJSON. No keys sent means a no-op, not an error.
	Sent []string `json:"-" jsonschema:"-"`
}

// UnmarshalJSON decodes the patch and remembers which keys were present.
func (p *ServicePatch) UnmarshalJSON(data []byte) error {
	type alias ServicePatch
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*p = ServicePatch(decoded)
	p.Sent = p.Sent[:0]

	var present map[string]json.RawMessage
	if err := json.Unmarshal(data, &present); err != nil {
		return err
	}
	for _, field := range []string{FieldDescription, FieldIcon, FieldColor, FieldHealth, FieldPort} {
		if _, sent := present[field]; sent {
			p.Sent = append(p.Sent, field)
		}
	}
	return nil
}

// SetString returns the patch with a text field set.
func (p ServicePatch) SetString(field, value string) ServicePatch {
	switch field {
	case FieldDescription:
		p.Description = pointerTo(value)
	case FieldIcon:
		p.Icon = pointerTo(value)
	case FieldColor:
		p.Color = pointerTo(value)
	case FieldHealth:
		p.Health = pointerTo(value)
	default:
		return p
	}
	return p.mark(field)
}

// SetPort returns the patch with the port hint set.
func (p ServicePatch) SetPort(port int) ServicePatch {
	p.Port = pointerTo(port)
	return p.mark(FieldPort)
}

// Clear returns the patch with a field marked for removal.
func (patch ServicePatch) Clear(field string) ServicePatch {
	switch field {
	case FieldDescription:
		patch.Description = nil
	case FieldIcon:
		patch.Icon = nil
	case FieldColor:
		patch.Color = nil
	case FieldHealth:
		patch.Health = nil
	case FieldPort:
		patch.Port = nil
	default:
		return patch
	}
	return patch.mark(field)
}

func pointerTo[T any](value T) *T { return &value }

func (p ServicePatch) mark(field string) ServicePatch {
	if slices.Contains(p.Sent, field) {
		return p
	}
	p.Sent = append(append([]string{}, p.Sent...), field)
	return p
}

// Empty reports whether the patch would change nothing.
func (p ServicePatch) Empty() bool { return len(p.Sent) == 0 }

// value returns the YAML node for one sent field, and whether the field
// removes the key instead.
func (p ServicePatch) value(field string) (node *yaml.Node, remove bool) {
	switch field {
	case FieldDescription:
		return textOrRemove(p.Description)
	case FieldIcon:
		return textOrRemove(p.Icon)
	case FieldColor:
		return textOrRemove(p.Color)
	case FieldHealth:
		if p.Health == nil {
			return nil, true
		}
		// Accept the canonical form so a client that read `tcp:5432` or
		// `command:pg_isready` can write it back unchanged.
		return healthNode(ParseHealth(*p.Health)), false
	case FieldPort:
		if p.Port == nil {
			return nil, true
		}
		return intNode(*p.Port), false
	}
	return nil, false
}

func textOrRemove(value *string) (*yaml.Node, bool) {
	if value == nil {
		return nil, true
	}
	return strNode(*value), false
}

func intNode(value int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(value)}
}

// ServiceEdit names the service a patch applies to.
type ServiceEdit struct {
	Name  string       `json:"name"`
	Patch ServicePatch `json:"patch"`
}

// ServiceNotFoundError reports a patch naming a service the file does not
// declare. It maps onto the contract's `not_found`.
type ServiceNotFoundError struct {
	Path string
	Name string
}

// Error names the missing service and the file it was looked up in.
func (e *ServiceNotFoundError) Error() string {
	return fmt.Sprintf("no service %q in %s", e.Name, e.Path)
}

// PatchServices applies metadata edits to a manifest and writes it back. It is
// EditServices with only the patch list filled in; ConfigEdit covers adding,
// renaming and removing services.
func PatchServices(path string, edits []ServiceEdit) (*Config, error) {
	return EditServices(path, ConfigEdit{Services: edits})
}

// applyPatch sets, replaces or removes each sent key of one service mapping.
func applyPatch(service *yaml.Node, patch ServicePatch) {
	for _, field := range patch.Sent {
		value, removing := patch.value(field)
		if removing {
			removeKey(service, field)
			continue
		}
		setKey(service, field, value)
	}
}

// setKey sets a key of one service mapping, in keyOrder.
func setKey(mapping *yaml.Node, key string, value *yaml.Node) {
	setKeyIn(mapping, key, value, keyOrder)
}

// setKeyIn replaces an existing key's value in place — keeping its comments
// and position — or inserts the pair where the order says it belongs.
func setKeyIn(mapping *yaml.Node, key string, value *yaml.Node, order []string) {
	if at := pairIndex(mapping, key); at >= 0 {
		previous := mapping.Content[at+1]
		value.HeadComment, value.LineComment, value.FootComment =
			previous.HeadComment, previous.LineComment, previous.FootComment
		mapping.Content[at+1] = value
		return
	}
	pair := []*yaml.Node{keyNode(key), value}
	mapping.Content = slices.Insert(mapping.Content, placement(mapping, key, order), pair...)
}

// placement is the Content index where a new key belongs: before the first key
// that sorts after it, otherwise at the end.
func placement(mapping *yaml.Node, key string, order []string) int {
	target := keyRank(key, order)
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if keyRank(mapping.Content[i].Value, order) > target {
			return i
		}
	}
	return len(mapping.Content)
}

func keyRank(key string, order []string) int {
	if at := slices.Index(order, key); at >= 0 {
		return at
	}
	return len(order)
}

func keyNode(key string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
}

// pairIndex is the Content index of a mapping key, or -1.
func pairIndex(mapping *yaml.Node, key string) int {
	if mapping == nil {
		return -1
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// removeKey drops a key and its value from a mapping.
func removeKey(mapping *yaml.Node, key string) {
	if at := pairIndex(mapping, key); at >= 0 {
		mapping.Content = slices.Delete(mapping.Content, at, at+2)
	}
}

// findService returns the mapping node of the named service.
func findService(services *yaml.Node, name string) *yaml.Node {
	at := serviceIndex(services, name)
	if at < 0 {
		return nil
	}
	return services.Content[at]
}

// documentRoot unwraps a document node.
func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind != yaml.DocumentNode {
		return doc
	}
	if len(doc.Content) == 0 {
		return nil
	}
	return doc.Content[0]
}

// mappingValue returns the value node for a key of a mapping, or nil.
func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	at := pairIndex(mapping, key)
	if at < 0 {
		return nil
	}
	return mapping.Content[at+1]
}

// encode renders the node tree with the two-space indentation manifests use.
func encode(doc *yaml.Node) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return nil, fmt.Errorf("rendering the config: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("rendering the config: %w", err)
	}
	return buffer.Bytes(), nil
}

// WriteConfigFile replaces a config atomically, keeping its current
// permissions. A caller that already rendered the bytes writes them through
// here rather than os.WriteFile, so a crash mid-write cannot leave half a file
// behind.
func WriteConfigFile(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return writeConfigAtomically(path, data, mode)
}

func writeConfigAtomically(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".option-berth-config-*")
	if err != nil {
		return configWriteError(path, err)
	}
	tempName := tmp.Name()
	defer os.Remove(tempName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return configWriteError(path, err)
	}
	if err := tmp.Close(); err != nil {
		return configWriteError(path, err)
	}
	if err := os.Chmod(tempName, mode); err != nil {
		return configWriteError(path, err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return configWriteError(path, err)
	}
	return nil
}

func configWriteError(path string, err error) error {
	return fmt.Errorf("writing %s: %w", path, err)
}
