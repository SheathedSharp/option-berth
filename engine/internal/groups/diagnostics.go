package groups

import (
	"fmt"
	"os"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// StrictEnv is the optional switch that turns ignored manifest fields into
// load errors. It is off unless the value is one of the accepted true
// spellings; without it a manifest keeps the historical behaviour of ignoring
// keys this version does not know.
const StrictEnv = "BERTH_STRICT_MANIFEST"

// StrictManifest reports whether manifest parsing rejects unknown fields.
func StrictManifest() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(StrictEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// FieldWarning locates ignored declaration keys without exposing their values.
// It is advisory; it does not change runtime parsing or execution semantics.
type FieldWarning struct {
	Line, Column int
	Field        string
}

func (w FieldWarning) String() string {
	return fmt.Sprintf("line %d:%d: unknown field %s (ignored)", w.Line, w.Column, w.Field)
}

// problem is the strict-mode form of a warning: the same location, without
// claiming the field was ignored, because in strict mode it was not.
func (w FieldWarning) problem() string {
	return fmt.Sprintf("line %d:%d: unknown field %s", w.Line, w.Column, w.Field)
}

// LoadWithWarnings validates and inspects the same bytes. Keys come from the
// existing declaration types, not a second manually maintained schema.
func LoadWithWarnings(path string) (*Config, []FieldWarning, error) {
	path = Canonical(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := Parse(path, data)
	if err != nil {
		return nil, nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, err
	}
	return cfg, fieldWarnings(&doc), nil
}

// fieldWarnings walks a parsed YAML document against Config's Go type and
// returns every key that type does not declare. Top-level extension keys are
// metadata: `x-` carries reusable anchors and `$schema` names this file's JSON
// Schema, so neither is a misspelling.
func fieldWarnings(doc *yaml.Node) []FieldWarning {
	root := documentRoot(doc)
	if root == nil {
		return nil
	}
	var warnings []FieldWarning
	type visit struct {
		node *yaml.Node
		typ  reflect.Type
	}
	seen := map[visit]bool{}
	cache := map[reflect.Type]map[string]reflect.Type{}
	var walk func(*yaml.Node, reflect.Type, string)
	walk = func(n *yaml.Node, typ reflect.Type, path string) {
		if n == nil {
			return
		}
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		key := visit{n, typ}
		if seen[key] {
			return
		}
		seen[key] = true
		if n.Kind == yaml.AliasNode {
			walk(n.Alias, typ, path)
			return
		}
		if typ.Kind() == reflect.Slice && n.Kind == yaml.SequenceNode {
			for i, item := range n.Content {
				walk(item, typ.Elem(), fmt.Sprintf("%s[%d]", path, i))
			}
			return
		}
		if typ.Kind() != reflect.Struct || n.Kind != yaml.MappingNode {
			return
		}
		fields := cache[typ]
		if fields == nil {
			fields = map[string]reflect.Type{}
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
				if f.PkgPath != "" || name == "-" {
					continue
				}
				if name == "" {
					name = strings.ToLower(f.Name)
				}
				fields[name] = f.Type
			}
			cache[typ] = fields
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.ShortTag() == "!!merge" {
				if v.Kind == yaml.SequenceNode {
					for _, item := range v.Content {
						walk(item, typ, path)
					}
				} else {
					walk(v, typ, path)
				}
				continue
			}
			field := k.Value
			if path != "" {
				field = path + "." + field
			}
			if ft, ok := fields[k.Value]; ok {
				walk(v, ft, field)
				continue
			}
			// Top-level extension maps may hold reusable YAML anchors. They never run.
			if path == "" && (strings.HasPrefix(k.Value, "x-") || k.Value == "$schema") {
				continue
			}
			warnings = append(warnings, FieldWarning{Line: k.Line, Column: k.Column, Field: field})
		}
	}
	walk(root, reflect.TypeOf(Config{}), "")
	return warnings
}

// strictProblems renders warnings as the load errors strict mode reports.
func strictProblems(warnings []FieldWarning) []string {
	problems := make([]string, len(warnings))
	for i, warning := range warnings {
		problems[i] = warning.problem()
	}
	return problems
}
