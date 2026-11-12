package groups

import (
	"maps"
	"slices"
)

// Stale compares current selections and bytes without modifying the index.
// Attribution refreshes directly in its shared pass, avoiding a read/check
// followed by a second full-index reload. This query remains useful to callers.
func (x *Index) Stale() bool {
	for dir, previous := range x.files {
		if !sameManifest(previous, selectManifest(dir)) {
			return true
		}
	}
	return false
}

// Known lists every config path the index has an opinion about, valid or not.
func (x *Index) Known() []string {
	paths := make(map[string]struct{}, len(x.configs)+len(x.invalid))
	for _, cfg := range x.configs {
		if cfg != nil {
			paths[cfg.Path] = struct{}{}
		}
	}
	for path := range x.invalid {
		paths[path] = struct{}{}
	}
	if len(paths) == 0 {
		return []string{}
	}
	return slices.Sorted(maps.Keys(paths))
}

// Named returns the valid config whose services are published as this group
// (see GroupOf). The deepest directory wins, matching Configs' ordering, so a
// nested project shadows the repository it sits in. Matching the group rather
// than the file's `name:` is what keeps a linked worktree's copy of a committed
// file from answering for the main checkout: the copy is
// `<project>@<worktree>`, and only the main checkout's file is `<project>`.
func (x *Index) Named(name string) (*Config, bool) {
	configs := x.Configs()
	for i := range configs {
		if x.GroupOf(configs[i]) == name {
			return configs[i], true
		}
	}
	return nil, false
}

// ByPath returns the valid config read from this file. Symlinks are resolved on
// both sides: the index stores the path the scanner walked to, while a client
// sends whatever the user typed — on macOS /var/… against /private/var/….
func (x *Index) ByPath(path string) (*Config, bool) {
	want := Canonical(path)
	if want != "" {
		for _, cfg := range x.configs {
			if cfg != nil && cfg.Path == want {
				return cfg, true
			}
		}
	}
	return nil, false
}

// Reload reconciles known directories and explicit roots. Unchanged files
// retain their parsed Config and derived claim index; only changed content is
// parsed. Previously selected directories remain discoverable after deletion,
// while definitely vanished directories release their observations.
func (x *Index) Reload(roots []string) (int, []InvalidConfig) {
	pass := newResolutionPass(x)
	for dir := range x.files {
		pass.probe(dir)
	}
	// Only Add's synthetic declarations need another discovery. Do not turn the
	// canonical target of a symlink into a second watched origin.
	backed := make(map[*Config]bool, len(x.files))
	for _, observed := range x.files {
		backed[observed.cfg] = true
	}
	for _, cfg := range x.Configs() {
		if cfg != nil && cfg.Dir != "" && !backed[cfg] {
			pass.probe(cfg.Dir)
		}
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		dir := pass.canonical(root)
		if _, seen := pass.probed[dir]; !seen {
			x.acceptManifest(dir, selectManifest(dir), true)
			pass.probed[dir] = struct{}{}
		}
		pass.observe(dir)
	}
	return len(x.configs), x.Invalid()
}

// LoadFile re-reads one config, replacing whatever the index held for it.
// `groups.config.set` calls it after a write so the next delta already carries
// the edited services.
func (x *Index) LoadFile(path string) error { return x.AddFile(path) }
