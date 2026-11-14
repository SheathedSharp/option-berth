package groups

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A reference is how a service's `prepare`, `cmd` and `env` name a port: its
// own with ${port} and ${url}, another's with ${<service>.port} and
// ${<service>.url}. The runtime service expands them when it starts the service, after
// the port of every service they name is known.
//
// ${worktree} is the fifth shape: a stable token for the checkout holding the
// manifest, so two worktrees of one repository keep separate data (a database
// name, a data directory) without sharing anything.
//
// Only those five shapes are references. Anything else between ${ and } —
// ${HOME}, ${FOO:-x} — stays exactly as written, because a `cmd` has never been
// expanded before and one that hands such a string to `sh -c` must keep
// working.
var refPattern = regexp.MustCompile(`\$\{([^{}$]*)\}`)

// envKey is what an `env:` key may be: a portable variable name.
var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// refField is the address a reference names. It is a closed set: the only
// values constructed anywhere are fieldPort and fieldURL.
type refField string

const (
	fieldPort   refField = "port"
	fieldURL    refField = "url"
	refWorktree          = "worktree"

	// worktreeTokenPrefix keeps the token usable as an unquoted SQL identifier,
	// a filesystem path element and an environment value: a letter first, then
	// lowercase hex.
	worktreeTokenPrefix = "wt_"
	worktreeTokenHex    = 12
)

// reference is one parsed ${...}: the service it names ("" is the service the
// text belongs to) and which of that service's addresses it asks for.
type reference struct {
	service string
	field   refField
}

// parseReference reads the inside of ${...} as a service address. It answers
// false for ${worktree} and for every shape this engine does not expand.
func parseReference(inner string) (reference, bool) {
	switch field := refField(inner); field {
	case fieldPort, fieldURL:
		return reference{field: field}, true
	}
	dot := strings.LastIndexByte(inner, '.')
	if dot <= 0 {
		return reference{}, false
	}
	switch field := refField(inner[dot+1:]); field {
	case fieldPort, fieldURL:
		return reference{service: inner[:dot], field: field}, true
	}
	return reference{}, false
}

// URL is what ${url} expands to for a port.
func URL(port int) string { return "http://localhost:" + strconv.Itoa(port) }

// Values are what the references in one service's text expand to.
type Values struct {
	// Self is the service the text belongs to: a bare ${port} and ${url} mean it.
	Self string
	// Worktree is the token ${worktree} expands to. Empty leaves the reference
	// as written, like a port reference naming a service with no known port.
	Worktree string
	// Ports are the ports of the services the text names.
	Ports map[string]int
}

// WorktreeToken is the stable identity ${worktree} expands to for the checkout
// rooted at root: "wt_" followed by twelve hex digits of a hash of the
// canonical path. The same checkout keeps the token across restarts; a main
// checkout and each linked worktree get different tokens, because their roots
// differ.
//
// The token is an identifier, not a path: embed it in one (`.data/${worktree}`)
// rather than expecting a directory. An empty root has no token.
func WorktreeToken(root string) string {
	canonical := Canonical(root)
	if canonical == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(canonical))
	return worktreeTokenPrefix + hex.EncodeToString(digest[:])[:worktreeTokenHex]
}

// WorktreeTokenFor is WorktreeToken for the checkout holding dir — the
// worktree's root when dir is inside one — and for dir itself when it is not
// inside a checkout.
func WorktreeTokenFor(dir string) string {
	identity, _ := WorktreeIdentityFor(dir)
	return identity
}

// WorktreeIdentityFor is WorktreeTokenFor with the canonical root the token was
// derived from. That root — the checkout's root, or dir itself outside a
// checkout — is the path whose existence decides whether the identity still
// has a checkout.
func WorktreeIdentityFor(dir string) (identity, root string) {
	co, inRepo := Locate(dir)
	if !inRepo {
		root = Canonical(dir)
	} else {
		root = co.Root
	}
	return WorktreeToken(root), root
}

// HasWorktreeRef reports whether text carries ${worktree}.
func HasWorktreeRef(text string) bool {
	return strings.Contains(text, "${"+refWorktree+"}")
}

// isReference reports whether the inside of ${...} is a shape the runtime service
// expands. ${worktree} names no service, so callers resolving service names
// use parseReference instead.
func isReference(inner string) bool {
	if inner == refWorktree {
		return true
	}
	_, ok := parseReference(inner)
	return ok
}

// Expand replaces the references in text with the values they name. A
// reference whose value is not known is left as written, and so is anything
// that is not a reference.
func Expand(text string, v Values) string {
	return refPattern.ReplaceAllStringFunc(text, func(match string) string {
		inner := match[2 : len(match)-1]
		if inner == refWorktree {
			if v.Worktree == "" {
				return match
			}
			return v.Worktree
		}
		ref, ok := parseReference(inner)
		if !ok {
			return match
		}
		owner := ref.service
		if owner == "" {
			owner = v.Self
		}
		port := v.Ports[owner]
		if port == 0 {
			return match
		}
		if ref.field == fieldURL {
			return URL(port)
		}
		return strconv.Itoa(port)
	})
}

// Refs lists the services the references in text name, in order and without
// repeats; a bare ${port} or ${url} names self. ${worktree} names no service
// and is never listed.
func Refs(text, self string) []string {
	var out []string
	for _, match := range refPattern.FindAllStringSubmatch(text, -1) {
		ref, ok := parseReference(match[1])
		if !ok {
			continue
		}
		name := ref.service
		if name == "" {
			name = self
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// MatchesExpanded reports whether actual could be pattern with its references
// expanded: the text around each reference must match exactly, and each
// reference stands for any non-empty text. `oberth start -- uvicorn --port
// 8123` matches a service whose cmd says `--port ${port}` this way.
func MatchesExpanded(pattern, actual string) bool {
	captures := refPattern.FindAllStringSubmatchIndex(pattern, -1)
	if len(captures) == 0 {
		return pattern == actual
	}
	var expr strings.Builder
	expr.WriteString("^")
	cursor := 0
	for _, span := range captures {
		expr.WriteString(regexp.QuoteMeta(pattern[cursor:span[0]]))
		if isReference(pattern[span[2]:span[3]]) {
			expr.WriteString(".+")
		} else {
			expr.WriteString(regexp.QuoteMeta(pattern[span[0]:span[1]]))
		}
		cursor = span[1]
	}
	expr.WriteString(regexp.QuoteMeta(pattern[cursor:]))
	expr.WriteString("$")
	compiled, err := regexp.Compile(expr.String())
	return err == nil && compiled.MatchString(actual)
}

// ServiceNamed returns the service with this name.
func (c *Config) ServiceNamed(name string) (Service, bool) {
	for i := range c.Services {
		if c.Services[i].Name == name {
			return c.Services[i], true
		}
	}
	return Service{}, false
}

// refProblems checks every reference in a service's prepare, cmd, health and
// env: each has to name a service in this file, a `machine:` reference, or a
// `workspace:` reference, and a service named directly has to have a port.
//
// Workspace references are read-only addresses resolved when a service starts,
// so a health command — re-expanded by the scanner without that resolution —
// may not name one.
func (c *Config) refProblems(s Service, where string) []string {
	var problems []string
	check := func(field, text string) {
		for _, match := range refPattern.FindAllStringSubmatch(text, -1) {
			ref, ok := parseReference(match[1])
			if !ok {
				continue
			}
			target, found := s, true
			if ref.service != "" {
				target, found = c.ServiceNamed(ref.service)
			}
			if found {
				if !target.HasPort() {
					problems = append(problems, fmt.Sprintf("%s: %s %s needs a port, and %s declares none", where, field, match[0], target.Name))
				}
				continue
			}
			if ref.service == "" {
				continue
			}
			if _, ok := c.MachineNamed(ref.service); ok {
				continue
			}
			if _, ok := c.WorkspaceNamed(ref.service); ok {
				if field == "health" {
					problems = append(problems, fmt.Sprintf("%s: health %s names a workspace reference; workspace addresses are expanded in prepare, cmd and env only", where, match[0]))
				}
				continue
			}
			problems = append(problems, fmt.Sprintf("%s: %s %s names no service %q", where, field, match[0], ref.service))
		}
	}
	check("prepare", s.Prepare)
	check("cmd", s.Cmd)
	check("health", s.Health)
	for _, key := range slices.Sorted(maps.Keys(s.Env)) {
		check("env "+key, s.Env[key])
	}
	return problems
}
