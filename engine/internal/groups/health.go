package groups

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// HealthKind is how a service's `health:` declaration is checked.
type HealthKind string

const (
	// HealthHTTP is the original form: an HTTP GET to the service's own port.
	HealthHTTP HealthKind = "http"
	// HealthTCP opens a TCP connection to the service's own port. It says the
	// socket accepts, not that the protocol behind it is ready.
	HealthTCP HealthKind = "tcp"
	// HealthCommand runs a foreground command in the service's cwd. Its exit
	// status is the verdict. This is the check for a database whose port opens
	// before it can answer queries (`pg_isready`).
	HealthCommand HealthKind = "command"
)

// HealthCheck is one parsed `health:` declaration.
type HealthCheck struct {
	Kind    HealthKind
	Path    string // HealthHTTP: the request path, always starting with "/"
	Port    int    // HealthTCP: the port to connect to
	Command string // HealthCommand: the command line, expanded when probed
}

// Empty reports whether the service declares no health check.
func (h HealthCheck) Empty() bool {
	return h.Kind == "" || (h.Kind == HealthHTTP && h.Path == "") ||
		(h.Kind == HealthTCP && h.Port == 0) || (h.Kind == HealthCommand && h.Command == "")
}

// Canonical is the value published as `services[].health`. HTTP keeps its
// historical bare path; the other kinds carry their kind as a prefix. The
// prefix is reserved: a scalar scalar health value that starts with `tcp:` or
// `command:` is refused by validation rather than guessed at.
func (h HealthCheck) Canonical() string {
	switch h.Kind {
	case HealthTCP:
		return "tcp:" + strconv.Itoa(h.Port)
	case HealthCommand:
		return "command:" + h.Command
	case HealthHTTP:
		return h.Path
	default:
		return ""
	}
}

// ParseHealth reads the published canonical form (and therefore also a plain
// HTTP path). It never fails: a value this package would have refused to write
// is treated as an HTTP path, which is exactly what the field meant before
// this union existed.
func ParseHealth(value string) HealthCheck {
	switch {
	case value == "":
		return HealthCheck{}
	case strings.HasPrefix(value, "tcp:"):
		port, err := strconv.Atoi(strings.TrimPrefix(value, "tcp:"))
		if err != nil {
			return HealthCheck{Kind: HealthHTTP, Path: value}
		}
		return HealthCheck{Kind: HealthTCP, Port: port}
	case strings.HasPrefix(value, "command:"):
		return HealthCheck{Kind: HealthCommand, Command: strings.TrimPrefix(value, "command:")}
	default:
		return HealthCheck{Kind: HealthHTTP, Path: value}
	}
}

// decodeHealthNode reads one YAML `health:` value. It accepts the original
// scalar path and the mapping form, and returns a line-numbered error instead
// of quietly decoding the mapping as something else.
func decodeHealthNode(n *yaml.Node) (HealthCheck, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!null":
			return HealthCheck{}, nil
		case "!!str":
			if healthIsReserved(n.Value) {
				return HealthCheck{}, fmt.Errorf("line %d: health %q starts with a reserved prefix; write it as a mapping", n.Line, n.Value)
			}
			return HealthCheck{Kind: HealthHTTP, Path: n.Value}, nil
		default:
			return HealthCheck{}, fmt.Errorf("line %d: health must be a path or a mapping with http, tcp or command", n.Line)
		}
	case yaml.MappingNode:
		var (
			check HealthCheck
			seen  string
		)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			switch k.Value {
			case "http", "tcp", "command":
				if seen != "" {
					return HealthCheck{}, fmt.Errorf("line %d: health has both %q and %q; use exactly one of http, tcp or command", k.Line, seen, k.Value)
				}
				seen = k.Value
				switch k.Value {
				case "http":
					if v.Kind != yaml.ScalarNode || v.ShortTag() != "!!str" || v.Value == "" {
						return HealthCheck{}, fmt.Errorf("line %d: health http must be a path", v.Line)
					}
					if healthIsReserved(v.Value) {
						return HealthCheck{}, fmt.Errorf("line %d: health path %q starts with a reserved prefix", v.Line, v.Value)
					}
					check = HealthCheck{Kind: HealthHTTP, Path: v.Value}
				case "tcp":
					if v.Kind != yaml.ScalarNode || v.ShortTag() != "!!int" {
						return HealthCheck{}, fmt.Errorf("line %d: health tcp must be a port number", v.Line)
					}
					port, err := strconv.Atoi(v.Value)
					if err != nil || port < 1 || port > 65535 {
						return HealthCheck{}, fmt.Errorf("line %d: health tcp port %q is out of range 1-65535", v.Line, v.Value)
					}
					check = HealthCheck{Kind: HealthTCP, Port: port}
				case "command":
					if v.Kind != yaml.ScalarNode || v.ShortTag() != "!!str" || strings.TrimSpace(v.Value) == "" {
						return HealthCheck{}, fmt.Errorf("line %d: health command must be a command line", v.Line)
					}
					check = HealthCheck{Kind: HealthCommand, Command: v.Value}
				}
			default:
				return HealthCheck{}, fmt.Errorf("line %d: unknown health key %q; use http, tcp or command", k.Line, k.Value)
			}
		}
		if seen == "" {
			return HealthCheck{}, fmt.Errorf("line %d: health mapping is empty; use http, tcp or command", n.Line)
		}
		return check, nil
	default:
		return HealthCheck{}, fmt.Errorf("line %d: health must be a path or a mapping with http, tcp or command", n.Line)
	}
}

// healthNode renders a check back as the YAML form that reads best: a bare
// path for HTTP, and the explicit mapping for the kinds that need an argument.
func healthNode(h HealthCheck) *yaml.Node {
	switch h.Kind {
	case HealthTCP:
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		m.Content = append(m.Content, strNode("tcp"),
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(h.Port)})
		return m
	case HealthCommand:
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		m.Content = append(m.Content, strNode("command"), strNode(h.Command))
		return m
	default:
		return strNode(h.Path)
	}
}

// healthIsReserved reports whether a scalar health value would be read back as
// a TCP or command form. Scalar values are HTTP paths by definition, so these
// prefixes would not survive a round trip.
func healthIsReserved(value string) bool {
	return strings.HasPrefix(value, "tcp:") || strings.HasPrefix(value, "command:")
}
