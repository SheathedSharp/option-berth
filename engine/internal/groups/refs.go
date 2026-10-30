package groups

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
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
// ${HOME}, ${FOO:-x} — is left exactly as written, because a `cmd` has never
// been expanded before and one that hands such a string to `sh -c` must keep
// working.
var refPattern = regexp.MustCompile(`\$\{([^{}$]*)\}`)

// envKey is what an `env:` key may be: a portable variable name.
var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
	switch inner {
	case refPort, refURL:
		return "", inner, true
	}
	for _, f := range []string{refPort, refURL} {
		if svc, found := strings.CutSuffix(inner, "."+f); found && svc != "" {
			return svc, f, true
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
	if co, ok := Locate(dir); ok {
		return WorktreeToken(co.Root)
	}
	return WorktreeToken(dir)
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
		port, ok := v.Ports[svc]
		if !ok || port == 0 {
			return m
		}
		if field == refURL {
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
	seen := map[string]bool{}
	for _, m := range refPattern.FindAllStringSubmatch(text, -1) {
		svc, _, ok := parseRef(m[1])
		if !ok {
			continue
		}
		if svc == "" {
			svc = self
		}
		if !seen[svc] {
			seen[svc] = true
			out = append(out, svc)
		}
	}
	return out
}

// MatchesExpanded reports whether actual could be pattern with its references
// expanded: the text around each reference must match exactly, and each
// reference stands for any non-empty text. `oberth start -- uvicorn --port
// 8123` matches a service whose cmd says `--port ${port}` this way.
func MatchesExpanded(pattern, actual string) bool {
	locs := refPattern.FindAllStringSubmatchIndex(pattern, -1)
	if len(locs) == 0 {
		return pattern == actual
	}
	var b strings.Builder
	b.WriteString("^")
	last := 0
	for _, loc := range locs {
		b.WriteString(regexp.QuoteMeta(pattern[last:loc[0]]))
		if isReference(pattern[loc[2]:loc[3]]) {
			b.WriteString(".+")
		} else {
			b.WriteString(regexp.QuoteMeta(pattern[loc[0]:loc[1]]))
		}
		last = loc[1]
	}
	b.WriteString(regexp.QuoteMeta(pattern[last:]))
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(actual)
}

// ServiceNamed returns the service with this name.
func (c *Config) ServiceNamed(name string) (Service, bool) {
	for _, s := range c.Services {
		if s.Name == name {
			return s, true
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
func (c *Config) refProblems(s Service, where string) []string {
	var out []string
	check := func(field, text string) {
		for _, m := range refPattern.FindAllStringSubmatch(text, -1) {
			svc, _, ok := parseRef(m[1])
			if !ok {
				continue
			}
			target, found := s, true
			if svc != "" {
				target, found = c.ServiceNamed(svc)
			}
			if found {
				if !target.HasPort() {
					out = append(out, fmt.Sprintf("%s: %s %s needs a port, and %s declares none", where, field, m[0], target.Name))
				}
				continue
			}
			if svc == "" {
				continue
			}
			if _, ok := c.MachineNamed(svc); ok {
				continue
			}
			if _, ok := c.WorkspaceNamed(svc); ok {
				if field == "health" {
					out = append(out, fmt.Sprintf("%s: health %s names a workspace reference; workspace addresses are expanded in prepare, cmd and env only", where, m[0]))
				}
				continue
			}
			out = append(out, fmt.Sprintf("%s: %s %s names no service %q", where, field, m[0], svc))
		}
	}
	check("prepare", s.Prepare)
	check("cmd", s.Cmd)
	check("health", s.Health)
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		check("env "+k, s.Env[k])
	}
	return out
}
