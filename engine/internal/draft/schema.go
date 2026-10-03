package draft

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DraftSchema is what a drafting run is asked to answer in. Both supported CLIs
// take a JSON Schema and guarantee the reply conforms to it (`claude
// --json-schema`, `codex exec --output-schema`), so the shape of a draft is
// stated once, here, instead of being asked for in prose and parsed back out of
// a fenced code block.
//
// Two of its shape rules are the platform's, not a preference, and both were
// learned by running it (2026-09-24, codex against its OpenAI backend, which
// validates in strict mode):
//
//   - **Every property is listed in `required`.** An optional field is written
//     as a union with null (`"type": ["string", "null"]`) and the answer carries
//     an explicit null. Leaving one out was refused outright: "Invalid schema
//     for response_format 'codex_output_schema': In context=('properties',
//     'services', 'items'), 'required' is required to be supplied and to be an
//     array including every key in properties. Missing 'cwd'."
//   - **No free-form maps.** `env` is a list of name/value pairs rather than an
//     object keyed by variable name: a map has no declared properties, and
//     strict mode requires them.
//
// It is deliberately the manifest's own fields plus two for provenance: `why`
// (where the command came from) and `verified` (what was actually seen). Both
// are rendered as comments above the entry they belong to — the draft is read
// by a person deciding whether to adopt it, and the provenance is the part that
// decides.
const DraftSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["name", "services", "machine", "notes"],
  "properties": {
    "name": {
      "type": "string",
      "description": "The manifest's name: for the group this project is known by."
    },
    "services": {
      "type": "array",
      "description": "One entry per service the project needs. Empty is a valid answer.",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["name", "prepare", "cmd", "cwd", "port", "port_auto", "env", "depends_on", "health", "why", "verified"],
        "properties": {
          "name": {"type": "string", "description": "Short name, unique in this file."},
          "prepare": {"type": ["string", "null"], "description": "A foreground build or code-generation command to run before cmd, when the runnable artifact must be rebuilt."},
          "cmd": {"type": "string", "description": "The command that brings the service back up, run from cwd. A foreground command: no nohup, no &."},
          "cwd": {"type": ["string", "null"], "description": "Directory to run in, relative to the project root. Null for the root."},
          "port": {"type": ["integer", "null"], "description": "The port it binds. Null when it binds none."},
          "port_auto": {"type": ["boolean", "null"], "description": "True to let option-berth pick the port (port: auto). Null otherwise."},
          "env": {
            "type": ["array", "null"],
            "description": "Environment added when option-berth starts it. ${port} and ${<service>.url} are substituted.",
            "items": {
              "type": "object",
              "additionalProperties": false,
              "required": ["name", "value"],
              "properties": {
                "name": {"type": "string"},
                "value": {"type": "string"}
              }
            }
          },
          "depends_on": {"type": ["array", "null"], "items": {"type": "string"}, "description": "Names of services that must be started first."},
          "health": {"type": ["string", "null"], "description": "An HTTP path that answers when it is up, e.g. /healthz."},
          "why": {"type": "string", "description": "Where this command comes from: the file and line you read it in, or why you concluded it."},
          "verified": {"type": ["string", "null"], "description": "What you saw when you checked it — \"already listening on 3000 when inspected\", \"started, port 3000 came up, stopped again\" — or \"unverified\"."}
        }
      }
    },
    "machine": {
      "type": ["array", "null"],
      "description": "Services on this machine the project depends on but does not own (a mysql the machine keeps running). A reference: option-berth never starts or stops one, so it carries no cmd.",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["name", "port"],
        "properties": {
          "name": {"type": "string", "description": "Short name, e.g. mysql."},
          "port": {"type": "integer", "description": "The port it listens on: what the dependency is matched against."}
        }
      }
    },
    "notes": {
      "type": ["array", "null"],
      "items": {"type": "string"},
      "description": "Anything the reader should know that does not belong to one service."
    }
  }
}`

// Draft is one drafting run's answer, decoded from the reply the CLI was asked
// to produce in DraftSchema.
type Draft struct {
	Name     string         `json:"name"`
	Services []DraftService `json:"services"`
	Machine  []DraftMachine `json:"machine"`
	Notes    []string       `json:"notes"`
}

// DraftMachine is one reference to a service the machine runs: the project
// needs it, option-berth does not start or stop it (decision 0010).
type DraftMachine struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

// DraftService is one proposed service plus the provenance that decides whether
// a person adopts it. The optional fields are pointers because the schema
// requires every key to be present and nullable: "absent" arrives as an
// explicit null, which decodes to nil here and to nothing in the file.
type DraftService struct {
	Name      string     `json:"name"`
	Prepare   *string    `json:"prepare"`
	Cmd       string     `json:"cmd"`
	Cwd       *string    `json:"cwd"`
	Port      *int       `json:"port"`
	PortAuto  *bool      `json:"port_auto"`
	Env       []DraftEnv `json:"env"`
	DependsOn []string   `json:"depends_on"`
	Health    *string    `json:"health"`
	Why       string     `json:"why"`
	Verified  *string    `json:"verified"`
}

// DraftEnv is one environment variable, as a pair rather than a map: strict
// mode requires declared properties on every object, and a map has none.
type DraftEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ParseDraft decodes one answer. Unknown fields are refused rather than
// ignored: a CLI that answers with something else entirely should say so here,
// not turn into a draft with silently missing services.
func ParseDraft(answer []byte) (*Draft, error) {
	dec := json.NewDecoder(bytes.NewReader(answer))
	dec.DisallowUnknownFields()
	var d Draft
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoDraft, err)
	}
	if strings.TrimSpace(d.Name) == "" {
		return nil, fmt.Errorf("%w: the answer names no project", ErrNoDraft)
	}
	for i, s := range d.Services {
		if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Cmd) == "" {
			return nil, fmt.Errorf("%w: service %d has no name or no cmd", ErrNoDraft, i+1)
		}
	}
	return &d, nil
}

// YAML renders the draft as the file a person reads and adopts — the manifest's
// fields in the order this repository writes them, each entry preceded by the
// provenance comments that came with it.
//
// Rendering here rather than in the model is the point: the values go through
// the YAML encoder, so a command containing `: ` or a quote comes out as valid
// YAML by construction, and the file's shape is the same one `init` writes.
func (d *Draft) YAML() ([]byte, error) {
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = append(root.Content, key("name"), scalar(d.Name))

	services := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, s := range d.Services {
		services.Content = append(services.Content, s.node())
	}
	root.Content = append(root.Content, key("services"), services)

	if len(d.Machine) > 0 {
		refs := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		refs.HeadComment = "Services this machine runs — the project needs them, option-berth does not start or stop them"
		for _, m := range d.Machine {
			n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			n.Content = append(n.Content, key("name"), scalar(m.Name), key("port"), number(m.Port))
			refs.Content = append(refs.Content, n)
		}
		root.Content = append(root.Content, key("machine"), refs)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	out := buf.String()
	for _, note := range d.Notes {
		out += "# note: " + strings.ReplaceAll(strings.TrimSpace(note), "\n", "\n# ") + "\n"
	}
	return []byte(out), nil
}

// node renders one service as a mapping node, with its provenance as a head
// comment so the reader sees why before what.
func (s DraftService) node() *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.HeadComment = s.provenance()

	add := func(k, v string) {
		m.Content = append(m.Content, key(k), scalar(v))
	}
	add("name", s.Name)
	if s.Prepare != nil && strings.TrimSpace(*s.Prepare) != "" {
		add("prepare", *s.Prepare)
	}
	add("cmd", s.Cmd)
	if s.Cwd != nil && *s.Cwd != "" {
		add("cwd", *s.Cwd)
	}
	switch {
	case s.PortAuto != nil && *s.PortAuto:
		add("port", "auto")
	case s.Port != nil && *s.Port != 0:
		m.Content = append(m.Content, key("port"), number(*s.Port))
	}
	if len(s.Env) > 0 {
		env := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		seen := map[string]bool{}
		for _, e := range s.Env {
			if e.Name == "" || seen[e.Name] {
				continue // a duplicate would make the mapping invalid YAML
			}
			seen[e.Name] = true
			env.Content = append(env.Content, key(e.Name), scalar(e.Value))
		}
		m.Content = append(m.Content, key("env"), env)
	}
	if len(s.DependsOn) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, dep := range s.DependsOn {
			seq.Content = append(seq.Content, scalar(dep))
		}
		m.Content = append(m.Content, key("depends_on"), seq)
	}
	if s.Health != nil && *s.Health != "" {
		add("health", *s.Health)
	}
	return m
}

// provenance is the comment block above one entry: where the command came from,
// and what was seen when it was checked.
func (s DraftService) provenance() string {
	var lines []string
	for _, field := range []struct{ label, text string }{
		{"why", s.Why},
		{"verified", deref(s.Verified)},
	} {
		text := strings.TrimSpace(field.text)
		if text == "" {
			continue
		}
		lines = append(lines, "# "+field.label+": "+firstLine(text))
		for _, extra := range restLines(text) {
			lines = append(lines, "# "+extra)
		}
	}
	return strings.Join(lines, "\n")
}

func key(k string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k} }
func scalar(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

// number is an integer scalar — a port written as a string is a manifest the
// loader refuses ("port must be a number or auto"), so the tag matters.
func number(n int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(n)}
}

// restLines is everything after the first line of a multi-line field, so the
// comment keeps the model's own wrapping instead of being flattened.
func restLines(s string) []string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) < 2 {
		return nil
	}
	out := make([]string, 0, len(lines)-1)
	for _, line := range lines[1:] {
		out = append(out, strings.TrimSpace(line))
	}
	return out
}

// deref reads a nullable field from the answer: absent arrives as an explicit
// null (the schema requires every key), and that is the same as empty here.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
