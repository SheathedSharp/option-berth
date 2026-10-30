package groups

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/invopop/jsonschema"
)

// ManifestSchemaID is the canonical identifier of the checked-in manifest
// schema. ManifestSchemaURL is the raw file an editor's YAML language server
// fetches; it is what the modeline written above generated manifests points at.
//
// The schema is generated from the same Go types parsing uses, so a key that
// appears here is exactly a key Load accepts. It is written to
// docs/schema/manifest.schema.json by the //go:generate directive in
// internal/daemon/rpc/schema.go (that command renders both schemas).
const (
	ManifestSchemaID  = "https://github.com/sheathedsharp/option-berth/engine/docs/schema/manifest.schema.json"
	ManifestSchemaURL = "https://raw.githubusercontent.com/SheathedSharp/option-berth/main/engine/docs/schema/manifest.schema.json"
)

// ManifestSchema reflects Config into the JSON Schema published for
// `oberth.yaml`. Unknown keys are refused (editors flag a typo before the file
// is adopted), except for the top-level `x-` extension namespace and the
// optional `$schema` metadata key.
func ManifestSchema() *jsonschema.Schema {
	r := &jsonschema.Reflector{
		FieldNameTag:              "yaml",
		AllowAdditionalProperties: false,
		ExpandedStruct:            true,
	}
	s := r.Reflect(&Config{})
	s.Version = jsonschema.Version
	s.ID = jsonschema.ID(ManifestSchemaID)
	s.Title = "option-berth manifest"
	s.Description = "An oberth.yaml manifest: the services one worktree declares. Generated from " +
		"the Go types in internal/groups; do not edit by hand. Run `go generate ./...`."
	// Name is optional: the loader falls back to the directory name.
	s.Required = withoutName(s.Required)
	if s.Properties != nil {
		s.Properties.Set("$schema", &jsonschema.Schema{
			Type:        "string",
			Description: "Optional JSON Schema URL for editor completion and validation.",
		})
	}
	// Top-level extension keys hold reusable anchors and never run. They are
	// part of the format, not misspellings.
	s.PatternProperties = map[string]*jsonschema.Schema{"^x-": jsonschema.TrueSchema}
	s.AdditionalProperties = jsonschema.FalseSchema

	if svc := s.Definitions["Service"]; svc != nil && svc.Properties != nil {
		svc.Properties.Set("port", servicePortSchema())
		svc.Properties.Set("health", serviceHealthSchema())
	}
	if ref := s.Definitions["MachineRef"]; ref != nil && ref.Properties != nil {
		if port, ok := ref.Properties.Get("port"); ok {
			setPortRange(port)
		}
	}
	if s.Properties != nil {
		if ports, ok := s.Properties.Get("ports"); ok && ports.Items != nil {
			setPortRange(ports.Items)
		}
		if worktree, ok := s.Properties.Get("worktree_ports"); ok {
			worktree.Minimum = json.Number("1")
			worktree.Maximum = json.Number(strconv.Itoa(MaxWorktreePorts))
		}
	}
	return s
}

// MarshalManifestSchema returns the canonical bytes written to
// docs/schema/manifest.schema.json.
func MarshalManifestSchema() []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(ManifestSchema()); err != nil {
		panic(fmt.Sprintf("groups: marshalling manifest schema: %v", err))
	}
	return buf.Bytes()
}

// servicePortSchema is the one field whose YAML shape is not its Go shape:
// `port:` is a number or the literal `auto`. Reflect alone would say integer
// only, so the union is stated here from PortAutoValue.
func servicePortSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Description: "The port the service binds (1-65535), or auto to let option-berth pick one.",
		AnyOf: []*jsonschema.Schema{
			{Type: "integer", Minimum: json.Number("1"), Maximum: json.Number("65535")},
			{Type: "string", Const: PortAutoValue},
		},
	}
}

// serviceHealthSchema states the `health:` union. The Go field is a string
// because that is the canonical published form, but the YAML file also accepts
// a one-key mapping, so reflect alone would reject every tcp and command
// check in an editor.
func serviceHealthSchema() *jsonschema.Schema {
	mapping := &jsonschema.Schema{
		Type:                 "object",
		Description:          "A TCP or command check. Use exactly one key.",
		AdditionalProperties: jsonschema.FalseSchema,
		MinProperties:        count(1),
		MaxProperties:        count(1),
		Properties:           jsonschema.NewProperties(),
	}
	mapping.Properties.Set("http", &jsonschema.Schema{
		Type: "string", Pattern: "^/", Description: "HTTP path, for example /healthz.",
	})
	mapping.Properties.Set("tcp", &jsonschema.Schema{
		Type: "integer", Minimum: json.Number("1"), Maximum: json.Number("65535"),
		Description: "Port to connect to. It must match the service's declared port.",
	})
	mapping.Properties.Set("command", &jsonschema.Schema{
		Type: "string", MinLength: count(1),
		Description: "Command to run in the service's cwd; exit 0 is healthy.",
	})
	return &jsonschema.Schema{
		Description: "Health check: an HTTP path, {tcp: port}, or {command: command line}.",
		AnyOf:       []*jsonschema.Schema{{Type: "string", Pattern: "^/"}, mapping},
	}
}

func count(n uint64) *uint64 { return &n }

func setPortRange(s *jsonschema.Schema) {
	s.Minimum = json.Number("1")
	s.Maximum = json.Number("65535")
}

func withoutName(required []string) []string {
	out := make([]string, 0, len(required))
	for _, name := range required {
		if name != "name" {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
