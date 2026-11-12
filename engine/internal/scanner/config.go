package scanner

import (
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// The daemon's `oberth.yaml` index lives on the scan loop because the loop is
// the one thing that reads it every tick, and the contract makes it live as
// long as the daemon. The group handlers reach it through these accessors, so
// there is never a second copy going stale behind the first.

// withIndex runs fn with the attribution lock held and the index in place.
// Every accessor below is one query against that index; keeping admission and
// creation in one helper is what stops an accessor from forgetting either.
func withIndex(l *Loop, fn func(*groups.Index)) {
	l.attr.mu.Lock()
	defer l.attr.mu.Unlock()
	l.ensureIndex()
	fn(l.attr.index)
}

// Configs returns every valid `oberth.yaml` the daemon knows, deepest first.
func (l *Loop) Configs() []*groups.Config {
	var configs []*groups.Config
	withIndex(l, func(index *groups.Index) { configs = index.Configs() })
	return configs
}

// ConfigNamed returns the config that names this group.
func (l *Loop) ConfigNamed(name string) (*groups.Config, bool) {
	var (
		cfg *groups.Config
		ok  bool
	)
	withIndex(l, func(index *groups.Index) { cfg, ok = index.Named(name) })
	return cfg, ok
}

// GroupOf returns the group a config's services are published under. For a
// config at a checkout's root that is the checkout's group, whatever the file's
// own `name:` says: a linked worktree's copy names `<project>@<worktree>`.
func (l *Loop) GroupOf(cfg *groups.Config) string {
	var group string
	withIndex(l, func(index *groups.Index) { group = index.GroupOf(cfg) })
	return group
}

// PlanGroupRename works out what `groups.rename` changes, against the groups
// the caller read and the index they were built from.
func (l *Loop) PlanGroupRename(gg []state.Group, from, to string) (*groups.RenamePlan, error) {
	var (
		plan *groups.RenamePlan
		err  error
	)
	withIndex(l, func(index *groups.Index) { plan, err = groups.PlanRename(gg, index, from, to) })
	return plan, err
}

// ConfigAt returns the config read from this file.
func (l *Loop) ConfigAt(path string) (*groups.Config, bool) {
	var (
		cfg *groups.Config
		ok  bool
	)
	withIndex(l, func(index *groups.Index) { cfg, ok = index.ByPath(path) })
	return cfg, ok
}

// ObserveConfig indexes the `oberth.yaml` files at and above a directory, the
// way a process cwd does during a scan. `groups.start --config-path` uses it so
// a project the daemon has never seen still starts.
func (l *Loop) ObserveConfig(dir string) {
	withIndex(l, func(index *groups.Index) { index.Observe(dir) })
}

// LoadConfig re-reads one config file into the index, replacing whatever was
// there. `groups.config.set` calls it after its write so the next delta already
// carries the edit.
func (l *Loop) LoadConfig(path string) error {
	var err error
	withIndex(l, func(index *groups.Index) { err = index.LoadFile(path) })
	return err
}

// ReloadConfigs re-reads every known root: the ones in the store and the ones
// the index already holds. Files created, edited or deleted since the daemon
// started are picked up here; the result is how many valid configs remain, plus
// the files that could not be used.
func (l *Loop) ReloadConfigs() (int, []groups.InvalidConfig) {
	var (
		valid   int
		invalid []groups.InvalidConfig
	)
	withIndex(l, func(index *groups.Index) {
		var roots []string
		if st := l.opts.Store; st != nil {
			known, err := st.Roots()
			if err != nil {
				l.opts.Logger.Warn("reading known oberth.yaml roots", "error", err)
			}
			roots = known
		}
		valid, invalid = index.Reload(roots)
	})
	return valid, invalid
}
