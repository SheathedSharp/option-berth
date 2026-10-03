package groups

// This is the pre-pass precedence chain from 0bab56d6, kept in tests only.
// It deliberately does not use resolutionPass, its memo tables or resolver.
import (
	"path/filepath"

	"github.com/sheathedsharp/option-berth/internal/state"
)

func referenceResolveOne(p *state.Port, runs Registry, index *Index) {
	co, inRepo := referenceProjectCheckout(p, index)
	p.ProjectRoot = nil // Re-attribution must not retain a previous checkout on a miss.
	if inRepo {
		r := co.Root
		p.ProjectRoot = &r
	}
	p.Group, p.GroupSource = nil, nil

	if runs != nil {
		if run, ok := runs.Run(*p); ok && run.Group != "" {
			group := run.Group
			if inRepo {
				group = referenceRunGroup(index, group, co)
			}
			assign(p, group, state.SourceStart)
			return
		}
	}
	// The deepest config claiming the port wins. If that one lies outside the
	// linked worktree the port runs in, none inside it claims the port — they
	// would be deeper — so the checkout's own group takes it below.
	if cfg, _, ok := referenceMatchPort(index, *p); ok && (!inRepo || referenceWithin(index, cfg, co)) {
		assign(p, referenceGroupOf(index, cfg), state.SourceFile)
		return
	}
	if inRepo {
		// Name first: naming probes the main checkout, which is where a
		// main-checkout port's own config may still be waiting to be read.
		name := referenceCheckoutName(index, co)
		source := state.SourceAuto
		if referenceCheckoutConfig(index, co) != nil {
			source = state.SourceFile
		}
		assign(p, name, source)
		return
	}
	if p.Docker != nil && p.Docker.ComposeProject != "" {
		assign(p, p.Docker.ComposeProject, state.SourceAuto)
	}
}

func referenceProjectCheckout(p *state.Port, index *Index) (Checkout, bool) {
	if p.Cwd != "" {
		if co, ok := Locate(p.Cwd); ok {
			return co, true
		}
	}
	if p.Docker != nil && p.Docker.ComposeProject != "" {
		if dir := index.ComposeDir(p.Docker.ComposeProject); dir != "" {
			return Locate(dir)
		}
	}
	return Checkout{}, false
}

func referenceFileProjectName(x *Index, main string) string {
	x.probeDir(main)
	if cfg := referenceNearest(x, main); cfg != nil {
		return cfg.Name
	}
	return filepath.Base(main)
}

func referenceProjectName(x *Index, main string) string {
	if alias := x.aliases[main]; alias != "" {
		return alias
	}
	return referenceFileProjectName(x, main)
}

func referenceCheckoutName(x *Index, co Checkout) string {
	name := referenceProjectName(x, co.Main)
	if co.Linked() {
		return name + "@" + co.Worktree
	}
	return name
}

func referenceGroupOf(x *Index, cfg *Config) string {
	co, ok := Locate(cfg.Dir)
	if !ok {
		return cfg.Name
	}
	if cfg.Dir == co.Root {
		return referenceCheckoutName(x, co)
	}
	if co.Linked() {
		return cfg.Name + "@" + co.Worktree
	}
	return cfg.Name
}

func referenceRunGroup(x *Index, group string, co Checkout) string {
	base := referenceFileProjectName(x, co.Main)
	alias := x.aliases[co.Main]
	if co.Linked() {
		switch group {
		case base, base + "@" + co.Worktree:
			return referenceCheckoutName(x, co)
		}
		if alias != "" && (group == alias || group == alias+"@"+co.Worktree) {
			return referenceCheckoutName(x, co)
		}
		return group
	}
	if group == base {
		return referenceProjectName(x, co.Main)
	}
	return group
}

func referenceCheckoutConfig(x *Index, co Checkout) *Config {
	cfg := referenceNearest(x, co.Root)
	if cfg == nil || !referenceWithin(x, cfg, co) {
		return nil
	}
	return cfg
}

func referenceWithin(x *Index, cfg *Config, co Checkout) bool {
	return !co.Linked() || Under(cfg.Dir, co.Root)
}

func referenceNearest(x *Index, dir string) *Config {
	if dir == "" {
		return nil
	}
	cur := Canonical(dir)
	for i := 0; i < maxWalk; i++ {
		if cfg, ok := x.configs[cur]; ok {
			return cfg
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil
		}
		cur = parent
	}
	return nil
}

func referenceMatchPort(x *Index, p state.Port) (*Config, string, bool) {
	candidates := x.claimsForPort(p.Port)
	cwd := p.Cwd
	if cwd == "" && p.Docker != nil {
		cwd = x.ComposeDir(p.Docker.ComposeProject)
	}
	if cwd != "" {
		for _, c := range candidates {
			if Under(cwd, c.cfg.Dir) {
				return c.cfg, c.service, true
			}
		}
		return nil, "", false
	}
	if len(candidates) == 1 {
		return candidates[0].cfg, candidates[0].service, true
	}
	return nil, "", false
}
