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
// own with ${port} and ${url}, another service's with ${<service>.port} and
// ${<service>.url}. The daemon expands them when it starts the service, after
// the port of every service they name is known.
//
// ${worktree} is the fifth shape: a stable token for the checkout holding the
// manifest, so two worktrees of one repository can keep separate data (a
// database name, a data directory) without sharing anything.
//
// Only those five shapes are references. Anything else between ${ and } —
// ${HOME}, ${FOO:-x} — is returned as written: a `cmd` was never expanded
// before, and one that hands such a string to `sh -c` must keep working.
var (
	refPattern = regexp.MustCompile(`\$\{([^{}$]*)\}`)
	// envKey is the shape of a portable variable name.
	envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

const (
	refPort     = "port"
	refURL      = "url"
	refWorktree = "worktree"

	// worktreeTokenPrefix keeps the token usable as an unquoted SQL identifier,
	// a filesystem path element and an environment value: a letter first, then
	// lowercase hex.
	worktreeTokenPrefix = "wt_"
)

// parseRef reads the inside of ${...} as a service port reference. service is
// "" for the service's own port; ok is false for anything that is not one —
// including ${worktree}, which names no service.
func parseRef(inner string) (service, field string, ok bool) {
	if inner == refPort || inner == refURL {
		return "", inner, true
	}
	for _, candidate := range []string{refPort, refURL} {
		service, found := strings.CutSuffix(inner, "."+candidate)
		if found && service != "" {
			return service, candidate, true
		}
	}
	return "", "", false
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
// rooted at root: "wt_" followed by twelve hex digits of a hash of the canonical
// path. The same checkout keeps the token across restarts; a main checkout and
// each linked worktree get different tokens, because their roots differ.
//
// The token is an identifier, not a path: embed it in one (`.data/${worktree}`)
// rather than expecting a directory. An empty root has no token.
func WorktreeToken(root string) string {
	root = Canonical(root)
	if root == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(root))
	return worktreeTokenPrefix + hex.EncodeToString(sum[:])[:12]
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
	if co, ok := Locate(dir); ok {
		root = co.Root
	} else {
		root = Canonical(dir)
	}
	return WorktreeToken(root), root
}

// HasWorktreeRef reports whether text carries the ${worktree} reference.
func HasWorktreeRef(text string) bool {
	return strings.Contains(text, "${"+refWorktree+"}")
}

// isReference reports whether the inside of ${...} is one of the shapes the
// daemon expands. ${worktree} is not a service, so callers that resolve service
// names must use parseRef instead.
func isReference(inner string) bool {
	if inner == refWorktree {
		return true
	}
	_, _, ok := parseRef(inner)
	return ok
}

// Expand replaces the references in text with the values they name. A
// reference whose value is not known is left as written, and so is anything
// that is not a reference.
func Expand(text string, v Values) string {
	return refPattern.ReplaceAllStringFunc(text, func(m string) string {
		inner := m[2 : len(m)-1]
		if inner == refWorktree {
			if v.Worktree == "" {
				return m
			}
			return v.Worktree
		}
		svc, field, ok := parseRef(inner)
		if !ok {
			return m
		}
		if svc == "" {
			svc = v.Self
		}
		port, known := v.Ports[svc]
		if !known || port < 1 {
			return m
		}
		if field != refURL {
			return strconv.Itoa(port)
		}
		return URL(port)
	})
}

// Refs lists the services the references in text name, in order and without
// repeats; a bare ${port} or ${url} names self. ${worktree} names no service
// and is never listed.
func Refs(text, self string) []string {
	names := []string{}
	seen := map[string]bool{}
	for _, match := range refPattern.FindAllStringSubmatch(text, -1) {
		name, _, isRef := parseRef(match[1])
		if !isRef {
			continue
		}
		if name == "" {
			name = self
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// MatchesExpanded reports whether actual could be pattern with its references
// expanded: the text around each reference must match exactly, and each
// reference stands for any non-empty text. `oberth start -- uvicorn --port
// 8123` matches a service whose cmd says `--port ${port}` this way.
func MatchesExpanded(pattern, actual string) bool {
	matches := refPattern.FindAllStringSubmatchIndex(pattern, -1)
	if len(matches) == 0 {
		return pattern == actual
	}
	var expression strings.Builder
	expression.WriteString("^")
	end := 0
	for _, match := range matches {
		expression.WriteString(regexp.QuoteMeta(pattern[end:match[0]]))
		if isReference(pattern[match[2]:match[3]]) {
			expression.WriteString(".+")
		} else {
			expression.WriteString(regexp.QuoteMeta(pattern[match[0]:match[1]]))
		}
		end = match[1]
	}
	expression.WriteString(regexp.QuoteMeta(pattern[end:]))
	expression.WriteString("$")
	compiled, err := regexp.Compile(expression.String())
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
// so a health command — which is re-expanded by the scanner without that
// resolution — may not name one.
func (c *Config) refProblems(service Service, where string) []string {
	var problems []string
	inspect := func(field, text string) {
		for _, match := range refPattern.FindAllStringSubmatch(text, -1) {
			name, _, isRef := parseRef(match[1])
			if !isRef {
				continue
			}
			resolved, found := service, true
			if name != "" {
				resolved, found = c.ServiceNamed(name)
			}
			if found {
				if !resolved.HasPort() {
					problems = append(problems, fmt.Sprintf(
						"%s: %s %s needs a port, and %s declares none",
						where, field, match[0], resolved.Name))
				}
				continue
			}
			if name == "" {
				continue
			}
			if _, ok := c.MachineNamed(name); ok {
				continue
			}
			if _, ok := c.WorkspaceNamed(name); ok {
				if field == "health" {
					problems = append(problems, fmt.Sprintf(
						"%s: health %s names a workspace reference; workspace addresses are expanded in prepare, cmd and env only",
						where, match[0]))
				}
				continue
			}
			problems = append(problems, fmt.Sprintf(
				"%s: %s %s names no service %q", where, field, match[0], name))
		}
	}
	inspect("prepare", service.Prepare)
	inspect("cmd", service.Cmd)
	inspect("health", service.Health)
	for _, key := range slices.Sorted(maps.Keys(service.Env)) {
		inspect("env "+key, service.Env[key])
	}
	return problems
}
