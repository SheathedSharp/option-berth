package groups

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// Index is everything the resolver knows about where projects live: the
// `oberth.yaml` files seen so far, the ones that failed to parse, and the
// working directory of each Compose project.
//
// It is in-memory and rebuilt per command in the CLI's direct-scan path; the
// daemon keeps one for its lifetime and the store step persists the known roots
// so a config is remembered across restarts.
type Index struct {
	configs map[string]*Config // config directory -> config
	invalid map[string]error   // config path -> parse/validation error
	compose map[string]string  // compose project -> working_dir label
	// files remembers only directories with a selected/explicitly requested
	// manifest. Contents are compared on every pass, never trusted by mtime.
	files   map[string]manifestObservation
	aliases map[string]string // main checkout root -> project name (groups.rename)

	// ordered is the stable deepest-first view used by all resolver lookups.
	// claimIndex narrows a port lookup to configs that actually declare that
	// port. Both are invalidated only when a config is added or removed; a scan
	// therefore pays the sorting and indexing cost once rather than once per
	// listener.
	ordered      []*Config
	orderedDirty bool
	claimIndex   map[int][]portClaim
	claimsDirty  bool
	claimVersion uint64 // changes only when the immutable claim lists are rebuilt
}

// portClaim is one declaration that a port belongs to a config: the service
// that declares it, or "" for a top-level `ports:` entry.
type portClaim struct {
	cfg     *Config
	service string
}

// claimLookup answers whether one candidate config owns a port for a cwd.
type claimLookup func(port int, cwd string, candidates []portClaim) (portClaim, bool)

// InvalidConfig is one config file that could not be used, reported by
// `oberth groups` and ignored by the resolver.
type InvalidConfig struct {
	Path string
	Err  error
}

// NewIndex returns an empty index.
func NewIndex() *Index {
	return &Index{
		configs:      map[string]*Config{},
		invalid:      map[string]error{},
		compose:      map[string]string{},
		files:        map[string]manifestObservation{},
		aliases:      map[string]string{},
		orderedDirty: true,
		claimsDirty:  true,
	}
}

// Add installs an already parsed, caller-owned declaration. This in-memory API
// does not claim to have read its file; AddFile is the disk-observing API.
func (x *Index) Add(cfg *Config) {
	if cfg == nil || cfg.Dir == "" {
		return
	}
	x.configs[cfg.Dir] = cfg
	x.orderedDirty, x.claimsDirty = true, true
	delete(x.invalid, cfg.Path)
}

// AddFile captures and validates one file, replacing its previous declaration
// even when the new bytes are invalid. A failed read remains retryable.
func (x *Index) AddFile(path string) error {
	if path == "" {
		return fmt.Errorf("no config path given")
	}
	dir := Canonical(filepath.Dir(path))
	path = filepath.Join(dir, filepath.Base(path))
	return x.acceptManifest(dir, readManifest(path), true)
}

// Observe discovers and refreshes manifests between dir and its checkout root.
// A pass deduplicates shared ancestors; no negative survives as evidence.
func (x *Index) Observe(dir string) {
	(&resolutionPass{index: x}).observe(dir)
}

// probeDir checks file selection AND bytes. Equal metadata is not evidence of
// equal content. It does not enumerate the directory or parse unchanged YAML.
func (x *Index) probeDir(dir string) {
	x.acceptManifest(dir, selectManifest(dir), false)
}

// AddComposeProject records the working directory of a Compose project, read
// from the `com.docker.compose.project.working_dir` label. It is what lets a
// Compose container merge into the git-root group of the repo it was started
// from instead of forming a group of its own.
func (x *Index) AddComposeProject(project, workingDir string) {
	if project == "" || workingDir == "" {
		return
	}
	x.compose[project] = workingDir
}

// ComposeDir returns the recorded working directory of a Compose project.
func (x *Index) ComposeDir(project string) string { return x.compose[project] }

// At returns the config in exactly this directory, or nil.
func (x *Index) At(dir string) *Config {
	if dir == "" {
		return nil
	}
	return x.configs[Canonical(dir)]
}

// Nearest returns the config in dir or the closest ancestor of it, or nil.
func (x *Index) Nearest(dir string) *Config {
	return (&resolutionPass{index: x}).nearest(dir)
}

// Configs returns every valid config, deepest directory first so that a nested
// project wins over the repository it sits in.
func (x *Index) Configs() []*Config {
	if x.orderedDirty {
		x.ordered = make([]*Config, 0, len(x.configs))
		for _, cfg := range x.configs {
			x.ordered = append(x.ordered, cfg)
		}
		slices.SortFunc(x.ordered, func(a, b *Config) int {
			if len(a.Dir) != len(b.Dir) {
				return len(b.Dir) - len(a.Dir)
			}
			return strings.Compare(a.Dir, b.Dir)
		})
		x.orderedDirty = false
	}
	return slices.Clone(x.ordered)
}

// Invalid returns the files that could not be used, sorted by path.
func (x *Index) Invalid() []InvalidConfig {
	out := make([]InvalidConfig, 0, len(x.invalid))
	for path, err := range x.invalid {
		out = append(out, InvalidConfig{Path: path, Err: err})
	}
	slices.SortFunc(out, func(a, b InvalidConfig) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// MatchPort finds the config that claims a port: one whose `ports:` list
// contains it, or one of whose services declares it, with the process cwd under
// the file's directory. The deepest matching config wins.
//
// A port with no cwd is not out of reach. Every platform can read a working
// directory now, but plenty of individual rows still arrive without one: a
// Docker container has no cwd of its own, and a process that denies the scanner
// access keeps its own. Requiring one left every `oberth.yaml` unable to claim
// the ports it declares — the group existed in the index and no listener ever
// joined it. When the cwd is missing the question that remains is still
// answerable: is there exactly one known config claiming this port? One is an
// answer. Two is a guess, and a guess is worse than no group at all.
func (x *Index) MatchPort(p state.Port) (*Config, string, bool) {
	return x.matchPort(p, func(_ int, cwd string, candidates []portClaim) (portClaim, bool) {
		for _, c := range candidates {
			if Under(cwd, c.cfg.Dir) {
				return c, true
			}
		}
		return portClaim{}, false
	})
}

// matchPort applies the same claim rule to standalone calls and a pass's
// normalized paths.
func (x *Index) matchPort(p state.Port, lookup claimLookup) (*Config, string, bool) {
	candidates := x.claimsForPort(p.Port)
	cwd := p.Cwd
	// A container's own cwd is unavailable, but its Compose working directory
	// is positive project evidence. Do not let the no-cwd uniqueness fallback
	// assign it to an unrelated manifest just because that file claims a port.
	if cwd == "" && p.Docker != nil {
		cwd = x.ComposeDir(p.Docker.ComposeProject)
	}
	if cwd == "" {
		if len(candidates) != 1 {
			return nil, "", false
		}
		return candidates[0].cfg, candidates[0].service, true
	}
	claim, ok := lookup(p.Port, cwd, candidates)
	if !ok {
		return nil, "", false
	}
	return claim.cfg, claim.service, true
}

// claimsForPort returns configs declaring port in the same deepest-first order
// as Configs. The no-cwd rule needs the candidate count to detect ambiguity;
// the cwd rule only walks this much smaller list and applies the same boundary
// check as before.
func (x *Index) claimsForPort(port int) []portClaim {
	if x.claimsDirty {
		x.claimIndex = make(map[int][]portClaim)
		for _, cfg := range x.Configs() {
			declared := make(map[int]bool, len(cfg.Services)+len(cfg.Ports))
			for _, svc := range cfg.Services {
				if svc.Port == 0 || declared[svc.Port] {
					continue
				}
				declared[svc.Port] = true
				x.claimIndex[svc.Port] = append(x.claimIndex[svc.Port], portClaim{cfg: cfg, service: svc.Name})
			}
			for _, port := range cfg.Ports {
				if port == 0 || declared[port] {
					continue
				}
				declared[port] = true
				x.claimIndex[port] = append(x.claimIndex[port], portClaim{cfg: cfg})
			}
		}
		x.claimsDirty = false
		x.claimVersion++
	}
	return x.claimIndex[port]
}

// Under reports whether path is dir or lives inside it.
//
// Both sides are canonicalised first, so a process working in /private/var/x
// under a project at /var/x is inside it — on macOS those are the same
// directory, and a listener would otherwise be dropped from its own project.
func Under(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	return underCanonical(Canonical(path), Canonical(dir))
}

func underCanonical(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
