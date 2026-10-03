package groups

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Declared is a service a project's own file states outright: a package.json
// dev script, a compose service. It is context for whoever writes the manifest,
// with the same standing as a candidate: `oberth init` hands it to Marshal
// as a comment. A declaration in a file that is *not* the manifest is not one of
// the project's services until a person writes it there.
type Declared struct {
	Name string
	Cmd  string
	Port int
	// Source is where the file says it, for the comment: a file name and the
	// key inside it.
	Source string
}

// Detect reads the files a project uses to *declare* what it runs, and reports
// what they state outright.
//
// It is deliberately small. A package manager's dev script and a compose
// file's services are declarations; a Makefile target, a Procfile line or an
// entry point buried in a framework is a guess about someone else's
// repository, and a table of those guesses is never finished. `oberth init
// draft` is what reads the rest — by handing the question to the user's own
// local coding agent and keeping the answer as a draft.
func Detect(root string) []Declared {
	var out []Declared
	if d, ok := detectNodeScript(root); ok {
		out = append(out, d)
	}
	return append(out, detectCompose(root)...)
}

// nodeScripts is the order package.json scripts are preferred in: the one a
// developer runs to work on the project.
var nodeScripts = []string{"dev", "start"}

// detectNodeScript reads package.json's dev script, run by whichever package
// manager the lockfile commits the project to.
func detectNodeScript(root string) (Declared, bool) {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return Declared{}, false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return Declared{}, false
	}
	for _, name := range nodeScripts {
		if strings.TrimSpace(pkg.Scripts[name]) == "" {
			continue
		}
		return Declared{
			Name:   name,
			Cmd:    packageManager(root) + " run " + name,
			Source: "package.json (scripts." + name + ")",
		}, true
	}
	return Declared{}, false
}

// packageManager is the one this project's lockfile commits it to.
func packageManager(root string) string {
	for _, lock := range []struct{ file, pm string }{
		{"pnpm-lock.yaml", "pnpm"},
		{"yarn.lock", "yarn"},
		{"bun.lockb", "bun"},
		{"bun.lock", "bun"},
	} {
		if _, err := os.Stat(filepath.Join(root, lock.file)); err == nil {
			return lock.pm
		}
	}
	return "npm"
}

// composeFiles are the names Docker Compose looks for, in its own order.
var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// detectCompose reads a compose file's services: one entry each, with the port
// it publishes. Services come back in name order — a YAML mapping has none of
// its own.
func detectCompose(root string) []Declared {
	for _, name := range composeFiles {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		var file struct {
			Services map[string]struct {
				Ports []yaml.Node `yaml:"ports"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal(data, &file); err != nil {
			return nil
		}
		names := make([]string, 0, len(file.Services))
		for n := range file.Services {
			names = append(names, n)
		}
		sort.Strings(names)

		out := make([]Declared, 0, len(names))
		for _, n := range names {
			entry := file.Services[n]
			d := Declared{Name: n, Cmd: "docker compose up " + n, Source: name}
			for _, p := range entry.Ports {
				if port := hostPort(p); port > 0 {
					d.Port = port
					break
				}
			}
			out = append(out, d)
		}
		return out
	}
	return nil
}

// hostPort reads the host side of one compose port entry, in either of the
// forms compose accepts: a string mapping, or the long syntax's `published`.
func hostPort(n yaml.Node) int {
	switch n.Kind {
	case yaml.ScalarNode:
		return hostPortOf(n.Value)
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "published" {
				return firstPort(n.Content[i+1].Value)
			}
		}
	}
	return 0
}

// hostPortOf reads "8080:80" and "127.0.0.1:8080:80" as 8080. A bare "80"
// publishes nothing fixed — compose picks the host port — so it is no port at
// all as far as a service declaration goes.
//
// A `${VAR:-8000}` is read as its default, after the substitution below. That
// matters more than it looks: without it the colon inside `${…}` was read as
// the mapping's separator, so every compose file that spells its ports that way
// — the common way to make them configurable — reported **no port at all**.
func hostPortOf(s string) int {
	parts := strings.Split(expandComposeDefaults(strings.Trim(s, `"'`)), ":")
	if len(parts) < 2 {
		return 0
	}
	return firstPort(parts[len(parts)-2])
}

// composeVar matches one `${VAR}`, `${VAR:-default}` or `${VAR-default}`.
var composeVar = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-?([^}]*))?\}`)

// expandComposeDefaults replaces every `${…}` with what the file itself states
// about it: its default, or nothing when it states none.
//
// Compose resolves these from the shell and the project's `.env` at `up` time.
// This reads a declaration, where neither is at hand, so the default is the only
// honest answer — and it is the one the file was written to fall back on.
func expandComposeDefaults(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return composeVar.ReplaceAllStringFunc(s, func(m string) string {
		return composeVar.FindStringSubmatch(m)[3]
	})
}

// firstPort reads a port, taking the first of a range and ignoring a protocol
// suffix: "3000-3005" and "3000/tcp" are both 3000.
func firstPort(s string) int {
	s = strings.Trim(strings.TrimSpace(s), `"'`)
	if i := strings.IndexAny(s, "-/"); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
}
