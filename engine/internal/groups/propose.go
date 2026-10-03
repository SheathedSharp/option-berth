package groups

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sheathedsharp/option-berth/internal/ports"
)

// header is written above a generated config so the next reader knows where it
// came from and that it is meant to be edited and committed.
const header = `# ` + ConfigName + ` — option-berth worktree service configuration, written by ` + "`oberth init`" + `.
# Edit and commit it: option-berth uses it to name this worktree and to know
# which services belong to it. https://github.com/sheathedsharp/option-berth
#
# The services list below is yours to write. option-berth starts and stops exactly
# what it declares, and it does not infer a service from a running process: a
# process's command line is a snapshot of one run — the arguments a shell, an IDE
# or a supervisor happened to pass — not a statement of how to bring the service
# back. A file full of entries guessed that way looks finished and starts nothing.
`

// Propose reports what a checkout is running, as the context for a config the
// user writes by hand. It declares no services at all.
//
// It used to: every port whose process worked inside the checkout became a
// service, with a command copied off the process and a name made unique by
// suffixing a port. The result read like a finished file and could not start
// anything — for a Homebrew-managed mysqld it copied a mysqld command line that
// nobody would type, and when the command line could not be read at all (a
// single unreadable pid failed the batched `ps` for every port) it wrote entries
// with no command whatsoever.
//
// What is left is the half a scan actually knows: a port was listening, by this
// process, in this directory. Marshal writes that as a comment, and the person
// reading it — who knows whether it is `brew services start mysql` or `pnpm run
// dev` — writes the service.
//
// index may be nil; it is only consulted for Compose working directories.
func Propose(root string, pp []ports.ListeningPort, index *Index) *Config {
	if index == nil {
		index = NewIndex()
	}
	_, worktree, _ := Find(root)
	cfg := &Config{
		Name: GroupName(root, worktree),
		Dir:  root,
		Path: filepath.Join(root, ConfigName),
	}

	candidates := make([]ports.ListeningPort, 0, len(pp))
	for _, p := range pp {
		if dir := portDir(p, index); dir != "" && Under(dir, root) {
			candidates = append(candidates, p)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Port != candidates[j].Port {
			return candidates[i].Port < candidates[j].Port
		}
		return candidates[i].PID < candidates[j].PID
	})

	seen := map[string]bool{}
	for _, p := range candidates {
		// One listener on several bind addresses is one candidate.
		if seen[serviceKey(p)] {
			continue
		}
		seen[serviceKey(p)] = true
		cfg.Candidates = append(cfg.Candidates, candidateOf(p, root, index))
	}
	return cfg
}

// serviceKey collapses the several bind addresses of one listener (IPv4 and
// IPv6, wildcard and loopback) into a single candidate.
func serviceKey(p ports.ListeningPort) string { return fmt.Sprintf("%d/%d", p.Port, p.PID) }

// candidateOf records what the scan saw for one listener, in the terms a reader
// can use: the port, a name they will recognise, where the process works
// relative to the project root, and enough to tell it apart from its neighbours
// (pid and the command line it was started with).
func candidateOf(p ports.ListeningPort, root string, index *Index) Candidate {
	c := Candidate{
		Port:    p.Port,
		Process: candidateName(p),
		PID:     p.PID,
		Command: p.Command,
	}
	if dir := portDir(p, index); dir != "" && dir != root {
		if rel, err := filepath.Rel(root, dir); err == nil && rel != "." {
			c.Cwd = filepath.ToSlash(rel)
		}
	}
	return c
}

// candidateName is the label a reader will recognise: a Compose service name and
// a run's tag both beat the process, which is all a plain listener has.
func candidateName(p ports.ListeningPort) string {
	if p.DockerComposeService != "" {
		return p.DockerComposeService
	}
	if p.Tag != "" {
		return p.Tag
	}
	return p.DisplayName()
}

// portDir is the directory a port belongs to: the process cwd, the Compose
// project's working directory for a container, or — when neither is at hand —
// the project root the resolver already attributed the port to.
//
// The last arm is what lets the daemon propose from a published snapshot.
// state.Port carries no Compose working directory (the resolver consumes it
// before a row is published), but it does carry project_root, which for a
// Compose container *is* the git root above that working directory. Without it
// a `groups.init` served by the daemon would silently drop every container the
// CLI sees. It only fires when the two better answers are absent, so the
// direct path is unchanged.
func portDir(p ports.ListeningPort, index *Index) string {
	if p.Cwd != "" {
		return p.Cwd
	}
	if p.DockerComposeProject != "" {
		if dir := index.ComposeDir(p.DockerComposeProject); dir != "" {
			return dir
		}
	}
	return p.ProjectRoot
}

// Marshal renders a config as the bytes to write to disk, with the generated
// header on top.
//
// It writes the worktree name, whatever services the caller put on the config —
// for a plain `oberth init`, none, and the list is left as `services: []`
// for the user to fill — and the machine references the caller carried over.
// Under them comes a comment with the shape of an entry to copy and whatever
// the scan saw listening inside the project.
func Marshal(cfg *Config) ([]byte, error) {
	var b strings.Builder
	b.WriteString(header)
	fmt.Fprintf(&b, "name: %s\n", yamlScalar(cfg.Name))
	if len(cfg.Services) == 0 {
		b.WriteString("services: []\n")
	} else {
		body, err := marshalServices(cfg.Services)
		if err != nil {
			return nil, err
		}
		b.Write(body)
	}
	if len(cfg.Machine) > 0 {
		body, err := marshalMachine(cfg.Machine)
		if err != nil {
			return nil, err
		}
		b.Write(body)
	}
	b.WriteString(serviceNotes(cfg))
	return []byte(b.String()), nil
}

// marshalServices renders the `services:` key at two-space indentation, the way
// the rest of this file's readers expect a hand-written list to look.
func marshalServices(services []Service) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(struct {
		Services []Service `yaml:"services"`
	}{services}); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// marshalMachine renders the `machine:` list — the services this machine runs
// that the project depends on — at the same two-space indentation as the
// services list above it.
func marshalMachine(refs []MachineRef) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(struct {
		Machine []MachineRef `yaml:"machine"`
	}{refs}); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// yamlScalar renders one string as the value side of a key, quoted when YAML
// would otherwise read it as something else. A project directory can be called
// anything, including `yes` or `a: b`.
func yamlScalar(s string) string {
	out, err := yaml.Marshal(s)
	if err != nil {
		return s
	}
	return strings.TrimSpace(string(out))
}

// serviceNotes is the comment block under the service list: the shape of an
// entry to copy, then what the scan saw listening inside the project.
//
// The second half is why the block exists. option-berth will not declare a service
// it cannot state a start command for, and the reader is the only one who can —
// so it hands over the facts (a port, a process, a directory) and stops there.
func serviceNotes(cfg *Config) string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("# A service, to copy:\n")
	b.WriteString("#\n")
	for _, line := range []string{
		`  - name: api`,
		`    prepare: mvn -DskipTests package  # optional: rebuild an artifact first`,
		`    cmd: pnpm run dev              # what brings it back up`,
		`    port: 8000                     # optional: the port it binds`,
		`    cwd: .                         # optional: where to run it, relative to this file`,
		`    health: /healthz               # optional: an HTTP path that says it is up`,
		`    env:                           # optional, added to its environment`,
		`      NODE_ENV: development`,
		``,
		`A service the machine owns (a database started at login, an already-running`,
		`redis) is not a service of this project: it goes in the file's own machine:`,
		`list as a reference — option-berth never starts or stops one, so it carries no`,
		`cmd:`,
		`  machine:`,
		`    - name: mysql`,
		`      port: 3306`,
	} {
		// Trailing spaces are trimmed: an empty line in the list above is a
		// paragraph break in the comment, not "# " with nothing after it.
		b.WriteString(strings.TrimRight("# "+line, " ") + "\n")
	}

	if len(cfg.Declared) > 0 {
		b.WriteString("#\n")
		b.WriteString("# Stated by this project's own files. A command, but again not a declaration:\n")
		b.WriteString("# a compose service is one line of somebody else's file, and copying it up is\n")
		b.WriteString("# your call — `docker compose up <one service>` starts that service's\n")
		b.WriteString("# dependencies too, and those may be services you declare yourself.\n")
		b.WriteString("#\n")
		width := 0
		for _, d := range cfg.Declared {
			if w := len(d.Name); w > width {
				width = w
			}
		}
		for _, d := range cfg.Declared {
			b.WriteString(fmt.Sprintf("#   %-*s  %s", width, d.Name, d.Cmd))
			if d.Port != 0 {
				b.WriteString(fmt.Sprintf("  —  port %d", d.Port))
			}
			b.WriteString("  [" + d.Source + "]\n")
		}
	}

	if len(cfg.Candidates) == 0 {
		b.WriteString("#\n")
		b.WriteString("# Nothing was listening inside this project when this file was written.\n")
		return b.String()
	}

	b.WriteString("#\n")
	b.WriteString("# Listening inside this project when this file was written. This is context to\n")
	b.WriteString("# write from, not a declaration — nothing here was added to the list for you.\n")
	b.WriteString("# The command line is how the process was started *this time*: it is there to\n")
	b.WriteString("# tell two listeners apart, not to be copied into `cmd:`.\n")
	b.WriteString("#\n")
	portWidth := 0
	for _, c := range cfg.Candidates {
		if w := len(strconv.Itoa(c.Port)); w > portWidth {
			portWidth = w
		}
	}
	for _, c := range cfg.Candidates {
		process := c.Process
		if process == "" {
			process = "?"
		}
		where := c.Cwd
		if where == "" {
			where = "."
		}
		b.WriteString(fmt.Sprintf("#   %*d  %-16s %s", portWidth, c.Port, process, where))
		if c.PID > 0 {
			b.WriteString(fmt.Sprintf("  pid %d", c.PID))
		}
		b.WriteString("\n")
		if c.Command != "" {
			b.WriteString("#       " + shortCommand(c.Command) + "\n")
		}
	}
	return b.String()
}

// commandWidth keeps a command line to one readable line, and commandTail says
// how much of the *end* to keep when it does not fit. Both ends matter here:
// `java -jar nacos.jar` differs near the front, while two `python -c '…'`
// listeners differ in the script's tail (the port it binds is in there). Cutting
// only the end left exactly the case this list exists for looking identical.
const (
	commandWidth = 110
	commandTail  = 34
)

// shortCommand collapses a command line's whitespace — ps output puts newlines
// where the arguments had them — and cuts it to one line, head and tail.
func shortCommand(cmd string) string {
	cmd = strings.Join(strings.Fields(cmd), " ")
	if len(cmd) <= commandWidth {
		return cmd
	}
	head := commandWidth - commandTail - 1
	return cmd[:head] + "…" + cmd[len(cmd)-commandTail:]
}
