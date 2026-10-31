package groups

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigName is the file a project commits at its repository root to name its
// group and describe its services. It is what option-berth writes; configNames is
// everything it reads.
const ConfigName = "oberth.yaml"

// LegacyConfigName is an older dotfile spelling of the same file. It is still
// read, and an edit goes back to it, but nothing new is created with it.
const LegacyConfigName = ".oberth.yaml"

// configNames are the spellings a directory is searched for, in order: the
// current name first, then the other extension, then the dotfiles it replaced.
// Only the first one present is used; `oberth doctor` reports the rest.
//
// **Nothing else is read.** Earlier releases also read the spellings this
// project wore under its previous names, and the upstream project's (`sonar.*`);
// both were dropped on 2026-09-24 (decision 0022). A spelling nobody writes is a
// compatibility branch nobody exercises — and a broken one fails *silently*,
// which is the failure this product likes least.
//
// Nothing is ever *created* under the non-current names either: ConfigName is
// the only name a new file gets.
var configNames = []string{
	ConfigName, "oberth.yml", LegacyConfigName, ".oberth.yml",
}

// ConfigNames returns the spellings a directory is searched for, in order.
func ConfigNames() []string { return append([]string(nil), configNames...) }

// IsConfigName reports whether a base name is one option-berth reads as a config.
func IsConfigName(base string) bool {
	for _, name := range configNames {
		if base == name {
			return true
		}
	}
	return false
}

// IsLegacyName reports whether a base name is one of the dotfile spellings.
func IsLegacyName(base string) bool {
	return IsConfigName(base) && strings.HasPrefix(base, ".")
}

// FilesIn lists the config files present in dir, in lookup order. The first is
// the one option-berth uses; any others are shadowed by it.
func FilesIn(dir string) []string {
	var out []string
	for _, name := range configNames {
		path := filepath.Join(dir, name)
		if info, err := os.Lstat(path); err == nil && !info.IsDir() {
			out = append(out, path)
		}
	}
	return out
}

// TargetIn is the file a write into dir goes to: the config already there, in
// whatever spelling it has, so an edit never leaves two files side by side —
// else a new ConfigName.
func TargetIn(dir string) string {
	if present := FilesIn(dir); len(present) > 0 {
		return present[0]
	}
	return filepath.Join(dir, ConfigName)
}

// Service is one entry of a config's `services:` list. Description, Icon and
// Color are user-authored metadata the daemon never infers and never
// interprets: it carries them onto state.Service so a client can render them
// (contract §13.1).
type Service struct {
	Name string `yaml:"name" jsonschema_description:"Short name, unique in this file."`
	// Prepare is an optional foreground command run immediately before Cmd.
	// It is useful for services whose runnable artifact (a jar, generated
	// binary, or bundle) must be rebuilt from the current checkout first.
	Prepare string `yaml:"prepare,omitempty" jsonschema_description:"Optional foreground command run immediately before cmd."`
	// PrepareInputs are the files Prepare reads, relative to the service's
	// cwd. When every one has the same contents as the last successful
	// Prepare, `up` skips the command. An empty list means "always run".
	PrepareInputs []string `yaml:"prepare_inputs,omitempty" jsonschema_description:"Files the prepare command reads, relative to cwd. When their contents are unchanged, up skips prepare."`
	Cmd           string   `yaml:"cmd,omitempty" jsonschema_description:"The command that brings the service back up, run from cwd."`
	Cwd           string   `yaml:"cwd,omitempty" jsonschema_description:"Working directory, relative to the directory holding oberth.yaml."`
	// Sources are the paths whose contents identify the code this service runs,
	// relative to its cwd. A missing key means cwd; an explicit empty list
	// disables the check for a service that reloads its own code.
	Sources []string `yaml:"sources,omitempty" jsonschema_description:"Files or directories whose contents identify the running code, relative to cwd. Omit to use cwd; set to [] to disable the check."`
	// EnvFile is a dotenv-style file read when the service starts. A relative
	// path is resolved against the directory holding the manifest, so
	// `../shared/dev.env` can point outside this checkout; an absolute path is
	// used as written. It is read-only: option-berth never creates or edits it.
	EnvFile string `yaml:"env_file,omitempty" jsonschema_description:"Dotenv-style file read when the service starts. Relative paths are resolved against the manifest's directory; the file is never written."`
	// Port is the port the service binds, or 0 when it declares none or when
	// PortAuto is set.
	Port int `yaml:"port,omitempty" jsonschema_description:"The port the service binds, or auto to let option-berth pick one."`
	// PortAuto is `port: auto`: the daemon picks the port when it starts the
	// service — the same one every time for the same checkout, and a
	// different one in every other checkout — and hands it over as PORT,
	// BERTH_PORT and ${port}.
	PortAuto    bool     `yaml:"-"`
	Health      string   `yaml:"health,omitempty" jsonschema_description:"Health check: an HTTP path, {tcp: port}, or {command: command line}."`
	Description string   `yaml:"description,omitempty" jsonschema_description:"User-authored metadata shown by clients."`
	Icon        string   `yaml:"icon,omitempty" jsonschema_description:"User-authored metadata shown by clients."`
	Color       string   `yaml:"color,omitempty" jsonschema_description:"User-authored metadata shown by clients."`
	DependsOn   []string `yaml:"depends_on,omitempty" jsonschema_description:"Names of services that must be started first."`
	// Env is added to the service's environment when option-berth starts it. Values
	// may refer to ports with ${port}, ${url}, ${<service>.port} and
	// ${<service>.url}, and to this checkout's stable identity with ${worktree}.
	Env map[string]string `yaml:"env,omitempty" jsonschema_description:"Environment added when option-berth starts the service. Values may use ${port}, ${url}, ${worktree}, and another service's port or url as ${name.port} or ${name.url}."`
}

// cloneStrings keeps nil and an explicit empty slice distinct: for `sources`
// nil means "the service directory" and [] means "do not check".
func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// PortAutoValue is the `port:` value that asks the daemon to pick the port.
const PortAutoValue = "auto"

// HasPort reports whether the service declares a port, fixed or auto.
func (s Service) HasPort() bool { return s.Port != 0 || s.PortAuto }

// UnmarshalYAML reads `port: auto` into PortAuto and every other key as usual.
// Any other non-number port is refused with a message that names both forms,
// rather than the decoder's own "cannot unmarshal !!str".
func (s *Service) UnmarshalYAML(n *yaml.Node) error {
	type plain Service
	if n.Kind != yaml.MappingNode {
		var p plain
		if err := n.Decode(&p); err != nil {
			return err
		}
		*s = Service(p)
		return nil
	}
	auto := false
	portLine, healthLine := 0, 0
	var health HealthCheck
	stripped := *n
	stripped.Content = make([]*yaml.Node, 0, len(n.Content))
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		// Check before removing auto: otherwise the custom decoder hides duplicates.
		if k.Value == "port" {
			if portLine != 0 {
				return fmt.Errorf(`line %d: mapping key "port" already defined at line %d`, k.Line, portLine)
			}
			portLine = k.Line
		}
		if k.Value == "health" {
			if healthLine != 0 {
				return fmt.Errorf(`line %d: mapping key "health" already defined at line %d`, k.Line, healthLine)
			}
			healthLine = k.Line
			h, err := decodeHealthNode(v)
			if err != nil {
				return err
			}
			health = h
			continue
		}
		if k.Value == "port" && v.Kind == yaml.ScalarNode {
			switch tag := v.ShortTag(); {
			case tag == "!!int" || tag == "!!null":
			case v.Value == PortAutoValue:
				auto = true
				continue
			default:
				return fmt.Errorf("line %d: port must be a number or %s, not %q", v.Line, PortAutoValue, v.Value)
			}
		}
		stripped.Content = append(stripped.Content, k, v)
	}
	var p plain
	if err := stripped.Decode(&p); err != nil {
		return err
	}
	*s = Service(p)
	s.PortAuto = auto
	s.Health = health.Canonical()
	return nil
}

// MarshalYAML writes PortAuto back as `port: auto`, where a number would be,
// and the TCP/command health forms back as mappings rather than their
// canonical wire strings.
func (s Service) MarshalYAML() (any, error) {
	type plain Service
	var n yaml.Node
	if err := n.Encode(plain(s)); err != nil {
		return nil, err
	}
	if s.PortAuto {
		at := 0
		for i := 0; i+1 < len(n.Content); i += 2 {
			switch n.Content[i].Value {
			case "name", "cmd", "cwd":
				at = i + 2
			}
		}
		pair := []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "port"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: PortAutoValue},
		}
		n.Content = append(n.Content[:at], append(pair, n.Content[at:]...)...)
	}
	if h := ParseHealth(s.Health); h.Kind == HealthTCP || h.Kind == HealthCommand {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "health" {
				n.Content[i+1] = healthNode(h)
				break
			}
		}
	}
	return &n, nil
}

// MaxWorktreePorts is the largest block `worktree_ports` may ask for. It is
// also the cap on a claim's explicit count, so a config can never ask for a
// block the claims manager would refuse.
const MaxWorktreePorts = 100

// FieldWorktreePorts is the top-level key naming the block size a worktree
// claims.
const FieldWorktreePorts = "worktree_ports"

// MachineRef is one entry of the file's top-level `machine:` list: a service
// that runs on this machine, which the project depends on but does not own.
//
// It is a reference, not a member: `up` and `down` never touch it, and it
// carries no cmd, because starting it is not this project's business (decision
// 0010). Declaring it is what lets the project show what it depends on, say
// whether that is listening, and mark the machine's rows with the projects
// that need them.
type MachineRef struct {
	// Name is what a reader calls the service ("mysql").
	Name string `yaml:"name" jsonschema_description:"How a reader calls the service, for example mysql."`
	// Port anchors the reference: it is what the dependency is aligned
	// against, and the row whose service-manager unit carries the rest of that
	// service's ports — mysql's 3306 and 33060 are one service, and one
	// declaration covers both.
	Port int `yaml:"port" jsonschema_description:"The port the dependency listens on. Required: it is what the reference is matched by."`
}

// WorkspaceRef is one entry of the file's top-level `workspace:` list: a
// service in another checkout, which this worktree's commands and environment
// may address but does not own.
//
// It is a reference, not a member: like `machine:`, `up` and `down` never
// touch it, and it carries no cmd. Unlike `machine:` the address is not fixed —
// the referenced checkout's own manifest decides the port, including
// `port: auto`. Resolution reads the other checkout's manifest and the daemon's
// runtime facts; it never starts or stops that service.
type WorkspaceRef struct {
	// Name is what this file calls the reference: `${backend.port}`,
	// `${backend.url}`. It must not collide with a service or machine name.
	Name string `yaml:"name" jsonschema_description:"Name this file uses for the reference, as in ${backend.port} or ${backend.url}."`
	// Worktree is the other checkout's root directory, relative to the
	// directory holding this manifest or absolute. A path to that checkout's
	// manifest is accepted too.
	Worktree string `yaml:"worktree" jsonschema_description:"The other checkout's root directory (or its manifest path), relative to this manifest's directory or absolute."`
	// Service names the service in the other checkout's manifest.
	Service string `yaml:"service" jsonschema_description:"The service name in the other checkout's manifest."`
}

// Config is a parsed `.oberth.yaml`. Path and Dir are filled by Load and are
// not part of the file format.
type Config struct {
	Name     string       `yaml:"name" jsonschema_description:"The group this project is known by. Defaults to the directory name when absent."`
	Services []Service    `yaml:"services,omitempty" jsonschema_description:"Services option-berth starts and stops for this project."`
	Machine  []MachineRef `yaml:"machine,omitempty" jsonschema_description:"Services the machine already runs that the project depends on. References only: option-berth never starts or stops them."`
	// Workspace are services in other checkouts this worktree may address by
	// ${<name>.port} and ${<name>.url}. They are read-only references: nothing
	// here starts or stops the other worktree.
	Workspace []WorkspaceRef `yaml:"workspace,omitempty" jsonschema_description:"Services in other checkouts, addressable by ${name.port} and ${name.url}. Read-only references: option-berth never starts or stops them."`
	Ports     []int          `yaml:"ports,omitempty" jsonschema_description:"Extra ports the project claims beyond its services."`
	// WorktreePorts is how many ports a claim for this project takes when the
	// caller does not name a count (step 5A.7). Nil means the key is absent
	// and the claims default applies.
	WorktreePorts *int `yaml:"worktree_ports,omitempty" jsonschema_description:"How many ports a linked-worktree claim takes by default."`

	Path string `yaml:"-"` // absolute path of the file it was read from
	Dir  string `yaml:"-"` // directory containing the file

	// Candidates are the listeners a scan found inside this project. They are
	// evidence for whoever writes the file, never a declaration: no field of
	// this struct is written to disk, and Marshal renders them as a comment.
	//
	// They exist because a port a scan saw is a fact, while the command that
	// would bring its service back is not — a process's argv is a snapshot of
	// one run. Propose used to turn the first into the second and wrote files
	// that looked complete and started nothing.
	Candidates []Candidate `yaml:"-"`

	// Declared are the services the project's *own* files state outright — a
	// package.json dev script, a compose service (internal/groups.Detect reads
	// them). Like Candidates they are material for whoever writes the file, and
	// Marshal renders them as a comment.
	//
	// `init` used to fold them into Services instead, on the argument that a
	// declaration states a command outright. It does — in a file that is not
	// this one. A command borrowed from a compose service starts the wrong
	// thing for a project that turned out to need `docker compose up db` and
	// its dependencies, and it arrived looking finished. Nothing becomes a
	// service but a line a person wrote here.
	Declared []Declared `yaml:"-"`
}

// Candidate is one listener found inside a project's directory. Process is a
// label for a reader — a Compose service name, a run's tag, or the process.
// Cwd is relative to the project root, and empty when the process works in the
// root itself.
//
// PID and Command are there to tell candidates apart, which is the whole job of
// this list: a project running five JVMs showed five identical lines (same
// process, same directory, only the port differing) and a reader had no way to
// know which was which. The command line says — it is what the process *was
// started with*, which is identification, not an instruction (the file's header
// says why it must not be copied into `cmd:`).
type Candidate struct {
	Port    int
	Process string
	Cwd     string
	PID     int
	Command string
}

// UsesAssignedPorts reports whether this file needs a daemon that knows the
// format v0.8.0 introduced: a `port: auto` service, or an `env:`, `prepare:`,
// `cmd:` or `health:` value carrying a ${...} reference (a port or the
// worktree identity).
func (c *Config) UsesAssignedPorts() bool {
	for _, s := range c.Services {
		if s.PortAuto || len(s.Env) > 0 {
			return true
		}
		if refPattern.MatchString(s.Prepare) || refPattern.MatchString(s.Cmd) || refPattern.MatchString(s.Health) {
			return true
		}
	}
	return false
}

// UsesManifestExtras reports whether the file uses a field an older daemon
// would silently ignore: `env_file` (the service would start without it) or
// `workspace` references (their addresses would stay unexpanded). The CLI
// checks the running daemon's capability before handing it such a file.
func (c *Config) UsesManifestExtras() bool {
	if len(c.Workspace) > 0 {
		return true
	}
	for _, s := range c.Services {
		if strings.TrimSpace(s.EnvFile) != "" {
			return true
		}
	}
	return false
}

// EnvFilePath is the absolute file a service's `env_file:` names. A relative
// path is resolved against the directory holding the manifest, which is what
// lets a worktree reference a shared file outside its own checkout.
func (c *Config) EnvFilePath(s Service) string {
	path := strings.TrimSpace(s.EnvFile)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(c.Dir, filepath.FromSlash(path))
}

// MachineNamed returns the machine reference with this name.
func (c *Config) MachineNamed(name string) (MachineRef, bool) {
	for _, m := range c.Machine {
		if m.Name == name {
			return m, true
		}
	}
	return MachineRef{}, false
}

// WorkspaceNamed returns the workspace reference with this name.
func (c *Config) WorkspaceNamed(name string) (WorkspaceRef, bool) {
	for _, w := range c.Workspace {
		if w.Name == name {
			return w, true
		}
	}
	return WorkspaceRef{}, false
}

// ConfigError reports every problem found in one file at once, so a user fixing
// a config sees the whole list instead of one error per run.
type ConfigError struct {
	Path     string
	Problems []string
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("%s: %s", e.Path, strings.Join(e.Problems, "; "))
}

// Load reads and validates a `.oberth.yaml`. The returned error is a
// *ConfigError listing every validation problem; callers report it and carry
// on, because an invalid config must never be fatal to a scan.
func Load(path string) (*Config, error) {
	// Canonical once, here: Path and Dir are the keys the index is built on,
	// so a config read through a symlinked path must land under the same key
	// as the same config found by walking a process's cwd.
	abs := Canonical(path)
	if abs == "" {
		return nil, fmt.Errorf("no config path given")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	return parse(abs, data)
}

// Parse validates config bytes as if they had been read from path. It is what
// a caller rendering a config in memory — `groups.init` writing a curated
// proposal — checks before putting anything on disk.
func Parse(path string, data []byte) (*Config, error) {
	abs := Canonical(path)
	if abs == "" {
		return nil, fmt.Errorf("no config path given")
	}
	return parse(abs, data)
}

func parse(abs string, data []byte) (*Config, error) {
	dir := filepath.Dir(abs)

	var doc yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, &ConfigError{Path: abs, Problems: []string{err.Error()}}
	}
	// A manifest is one document. Never silently discard trailing service declarations.
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		problem := "expected a single YAML document"
		if err != nil {
			problem += ": " + err.Error()
		}
		return nil, &ConfigError{Path: abs, Problems: []string{problem}}
	}
	if err := rejectShareKey(&doc); err != nil {
		return nil, &ConfigError{Path: abs, Problems: []string{err.Error()}}
	}
	if err := rejectStopKey(&doc); err != nil {
		return nil, &ConfigError{Path: abs, Problems: []string{err.Error()}}
	}
	// Optional strict mode: a key this version does not know is an error
	// instead of something to ignore. Off by default so an older installed
	// CLI cannot silently refuse a manifest that only gained fields.
	if StrictManifest() {
		if problems := strictProblems(fieldWarnings(&doc)); len(problems) > 0 {
			return nil, &ConfigError{Path: abs, Problems: problems}
		}
	}

	cfg := &Config{Path: abs, Dir: dir}
	if err := doc.Decode(cfg); err != nil {
		return nil, &ConfigError{Path: abs, Problems: []string{err.Error()}}
	}
	cfg.Path, cfg.Dir = abs, dir
	if cfg.Name == "" {
		cfg.Name = filepath.Base(dir)
	}

	if problems := cfg.validate(); len(problems) > 0 {
		return nil, &ConfigError{Path: abs, Problems: problems}
	}
	return cfg, nil
}

// validate implements the rules from the daemon spec: a usable group name,
// unique service names, cwd confined to the file's directory, ports in range
// and an acyclic depends_on graph.
func (c *Config) validate() []string {
	var problems []string

	if c.Name == "" {
		problems = append(problems, "name is empty")
	} else if i := strings.IndexAny(c.Name, "/\\ \t\n"); i >= 0 {
		problems = append(problems, fmt.Sprintf("name %q contains %q; names may not contain slashes or whitespace", c.Name, c.Name[i:i+1]))
	}

	for _, p := range c.Ports {
		if p < 1 || p > 65535 {
			problems = append(problems, fmt.Sprintf("ports: %d is out of range 1-65535", p))
		}
	}
	if n := c.WorktreePorts; n != nil && (*n < 1 || *n > MaxWorktreePorts) {
		problems = append(problems, fmt.Sprintf("%s: %d is out of range 1-%d", FieldWorktreePorts, *n, MaxWorktreePorts))
	}

	seen := map[string]bool{}
	for i, s := range c.Services {
		where := fmt.Sprintf("services[%d]", i)
		if s.Name == "" {
			problems = append(problems, where+": name is empty")
		} else {
			where = "service " + s.Name
			if seen[s.Name] {
				problems = append(problems, fmt.Sprintf("service %s: duplicate name", s.Name))
			}
			seen[s.Name] = true
		}
		if s.Port != 0 && (s.Port < 1 || s.Port > 65535) {
			problems = append(problems, fmt.Sprintf("%s: port %d is out of range 1-65535", where, s.Port))
		}
		if s.Cwd != "" && !c.cwdInside(s.Cwd) {
			problems = append(problems, fmt.Sprintf("%s: cwd %q escapes the directory holding %s", where, s.Cwd, ConfigName))
		}
		for key := range s.Env {
			if !envKey.MatchString(key) {
				problems = append(problems, fmt.Sprintf("%s: env %q is not a variable name", where, key))
			}
		}
		if strings.ContainsRune(s.EnvFile, 0) {
			problems = append(problems, where+": env_file contains a NUL byte")
		}
		problems = append(problems, c.prepareInputProblems(s, where)...)
		problems = append(problems, c.sourceProblems(s, where)...)
		problems = append(problems, c.healthProblems(s, where)...)
		problems = append(problems, c.refProblems(s, where)...)
	}

	// `machine:` entries are references to services on this machine, not
	// members of this project (decision 0010): each says what it is called and
	// which port it is anchored to.
	machines := map[string]bool{}
	for i, m := range c.Machine {
		where := fmt.Sprintf("machine[%d]", i)
		if m.Name == "" {
			problems = append(problems, where+": name is empty")
		} else {
			where = "machine " + m.Name
			if seen[m.Name] {
				problems = append(problems, fmt.Sprintf("%s: %s is already a service in this file", where, m.Name))
			}
			if machines[m.Name] {
				problems = append(problems, where+": duplicate name")
			}
			machines[m.Name] = true
		}
		switch {
		case m.Port == 0:
			problems = append(problems, where+": port is required — it is what the reference is matched by")
		case m.Port < 1 || m.Port > 65535:
			problems = append(problems, fmt.Sprintf("%s: port %d is out of range 1-65535", where, m.Port))
		}
	}

	// `workspace:` holds read-only references to services in other checkouts.
	// Their names live in the same `${<name>.port}` namespace as this file's
	// services and machine references, so the three may not collide.
	workspaces := map[string]bool{}
	for i, w := range c.Workspace {
		where := fmt.Sprintf("workspace[%d]", i)
		if w.Name == "" {
			problems = append(problems, where+": name is empty")
		} else {
			where = "workspace " + w.Name
			if seen[w.Name] {
				problems = append(problems, fmt.Sprintf("%s: %s is already a service in this file", where, w.Name))
			}
			if machines[w.Name] {
				problems = append(problems, fmt.Sprintf("%s: %s is already a machine reference in this file", where, w.Name))
			}
			if workspaces[w.Name] {
				problems = append(problems, where+": duplicate name")
			}
			workspaces[w.Name] = true
		}
		if strings.TrimSpace(w.Worktree) == "" {
			problems = append(problems, where+": worktree is required")
		}
		if strings.TrimSpace(w.Service) == "" {
			problems = append(problems, where+": service is required")
		}
	}

	for _, s := range c.Services {
		for _, dep := range s.DependsOn {
			if !seen[dep] {
				problems = append(problems, fmt.Sprintf("service %s: depends_on %q is not a service in this file", s.Name, dep))
			}
		}
	}
	if cycle := c.findCycle(); len(cycle) > 0 {
		problems = append(problems, "depends_on has a cycle: "+strings.Join(cycle, " -> "))
	}

	sort.Strings(problems)
	return problems
}

// cwdInside reports whether a service cwd stays inside the config's directory.
// Absolute paths are rejected outright: the field is documented as relative to
// the file.
func (c *Config) cwdInside(cwd string) bool {
	if filepath.IsAbs(cwd) || strings.HasPrefix(cwd, "/") || strings.HasPrefix(cwd, `\`) {
		return false
	}
	rel := filepath.Clean(filepath.FromSlash(cwd))
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// prepareInputProblems checks the files a prepare step declares. They are read
// relative to the service's cwd, and they must stay inside the directory that
// holds the manifest: a manifest may not hash files anywhere on the machine.
// Declaring inputs without a command is a contradiction, not a harmless no-op.
func (c *Config) prepareInputProblems(s Service, where string) []string {
	if len(s.PrepareInputs) == 0 {
		return nil
	}
	if strings.TrimSpace(s.Prepare) == "" {
		return []string{where + ": prepare_inputs needs a prepare command"}
	}
	var problems []string
	for _, input := range s.PrepareInputs {
		switch {
		case strings.TrimSpace(input) == "":
			problems = append(problems, where+": prepare_inputs has an empty path")
		case filepath.IsAbs(input) || strings.HasPrefix(input, "/") || strings.HasPrefix(input, `\`):
			problems = append(problems, fmt.Sprintf("%s: prepare_inputs path %q must be relative to cwd", where, input))
		case !c.cwdInside(filepath.Join(s.Cwd, input)):
			problems = append(problems, fmt.Sprintf("%s: prepare_inputs path %q escapes the directory holding %s", where, input, ConfigName))
		}
	}
	return problems
}

// sourceProblems checks the paths whose contents identify the running code.
// They are read relative to the service's cwd, and like prepare_inputs they
// may not reach anywhere on the machine. An explicit empty list is valid: it
// opts a hot-reloading service out of the check.
func (c *Config) sourceProblems(s Service, where string) []string {
	if s.Sources == nil {
		return nil
	}
	var problems []string
	for _, source := range s.Sources {
		switch {
		case strings.TrimSpace(source) == "":
			problems = append(problems, where+": sources has an empty path")
		case filepath.IsAbs(source) || strings.HasPrefix(source, "/") || strings.HasPrefix(source, `\`):
			problems = append(problems, fmt.Sprintf("%s: sources path %q must be relative to cwd", where, source))
		case !c.cwdInside(filepath.Join(s.Cwd, source)):
			problems = append(problems, fmt.Sprintf("%s: sources path %q escapes the directory holding %s", where, source, ConfigName))
		}
	}
	return problems
}

// healthProblems checks the parsed declaration against the service it belongs
// to. A scalar that reads like the canonical tcp/command form is refused:
// scalars are HTTP paths, so accepting it here would write a value that parses
// back differently.
func (c *Config) healthProblems(s Service, where string) []string {
	if s.Health == "" {
		return nil
	}
	h := ParseHealth(s.Health)
	if h.Kind == HealthHTTP && healthIsReserved(s.Health) {
		return []string{fmt.Sprintf("%s: health %q starts with a reserved prefix; write it as a mapping", where, s.Health)}
	}
	switch h.Kind {
	case HealthHTTP:
		if !strings.HasPrefix(h.Path, "/") {
			return []string{fmt.Sprintf("%s: health path %q must start with /", where, h.Path)}
		}
	case HealthTCP:
		if h.Port < 1 || h.Port > 65535 {
			return []string{fmt.Sprintf("%s: health tcp port %d is out of range 1-65535", where, h.Port)}
		}
		switch {
		case s.PortAuto:
			return []string{fmt.Sprintf("%s: health tcp needs a fixed port; a port: auto service should use a command check", where)}
		case s.Port == 0:
			return []string{fmt.Sprintf("%s: health tcp needs the service to declare a port", where)}
		case s.Port != h.Port:
			return []string{fmt.Sprintf("%s: health tcp port %d does not match the service's port %d", where, h.Port, s.Port)}
		}
	case HealthCommand:
		if strings.TrimSpace(h.Command) == "" {
			return []string{where + ": health command is empty"}
		}
	}
	return nil
}

// ServiceDir returns the absolute working directory for a service.
func (c *Config) ServiceDir(s Service) string {
	if s.Cwd == "" {
		return c.Dir
	}
	return filepath.Join(c.Dir, filepath.FromSlash(s.Cwd))
}

// findCycle returns one depends_on cycle as a readable chain, or nil.
func (c *Config) findCycle() []string {
	deps := map[string][]string{}
	for _, s := range c.Services {
		deps[s.Name] = append(deps[s.Name], s.DependsOn...)
	}
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	var walk func(string) []string
	walk = func(n string) []string {
		color[n] = grey
		stack = append(stack, n)
		for _, d := range deps[n] {
			switch color[d] {
			case grey:
				// Cut the stack down to the repeated node.
				for i, s := range stack {
					if s == d {
						return append(append([]string{}, stack[i:]...), d)
					}
				}
				return []string{d, d}
			case white:
				if cyc := walk(d); cyc != nil {
					return cyc
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return nil
	}
	names := make([]string, 0, len(deps))
	for n := range deps {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if color[n] == white {
			if cyc := walk(n); cyc != nil {
				return cyc
			}
		}
	}
	return nil
}

// rejectStopKey fails a config that still carries the old `stop:` key. It was
// replaced by the top-level `machine:` list (decision 0010): a service the
// machine owns is a reference to it, not a service of this project — and `up`
// must not start it either, which is the half the old key got wrong. Ignoring
// the key would leave the file asserting something the engine no longer does.
func rejectStopKey(doc *yaml.Node) error {
	root := documentRoot(doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "services" {
			continue
		}
		list := root.Content[i+1]
		if list.Kind != yaml.SequenceNode {
			continue
		}
		for _, item := range list.Content {
			if item.Kind != yaml.MappingNode || !hasKey(item, "stop") {
				continue
			}
			where := "a service"
			if name := mappingName(item); name != "" {
				where = "service " + name
			}
			return fmt.Errorf("%s has a `stop:` key: it was replaced by the top-level `machine:` list "+
				"(decision 0010). A service the machine owns is a reference now — one entry per service, "+
				"`- name: mysql` with `port: 3306` — and `up` does not start it either", where)
		}
	}
	return nil
}

// shareKeys are the keys a `.oberth.yaml` may not carry, at the top level or on
// a service. `share:` is the verb; `expose:` is its old name, still refused so
// a file written against the old docs gets the same answer.
var shareKeys = []string{"share", "expose"}

// rejectShareKey fails a config that carries a `share:` or an `expose:` key.
// Sharing is not part of `.oberth.yaml`; saying so explicitly is friendlier
// than ignoring a key the author expects to do something.
func rejectShareKey(doc *yaml.Node) error {
	root := documentRoot(doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	if key := firstShareKey(root); key != "" {
		return shareKeyError(key, "")
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "services" {
			continue
		}
		list := root.Content[i+1]
		if list.Kind != yaml.SequenceNode {
			continue
		}
		for _, item := range list.Content {
			if item.Kind != yaml.MappingNode {
				continue
			}
			if key := firstShareKey(item); key != "" {
				return shareKeyError(key, mappingName(item))
			}
		}
	}
	return nil
}

func firstShareKey(mapping *yaml.Node) string {
	for _, k := range shareKeys {
		if hasKey(mapping, k) {
			return k
		}
	}
	return ""
}

func shareKeyError(key, service string) error {
	where := ConfigName
	if service != "" {
		where = "service " + service
	}
	article := "a"
	if key == "expose" {
		article = "an"
	}
	return fmt.Errorf("%s has %s `%s:` key: sharing is not configured in %s. "+
		"A share is created at runtime with `option-berth share`, not in this file", where, article, key, ConfigName)
}

func hasKey(mapping *yaml.Node, key string) bool {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return true
		}
	}
	return false
}

func mappingName(mapping *yaml.Node) string {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "name" {
			return mapping.Content[i+1].Value
		}
	}
	return ""
}
