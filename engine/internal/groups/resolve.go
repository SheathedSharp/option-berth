package groups

import (
	"slices"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// Registry attributes a port to a process option-berth started. It hands back
// the whole run rather than just its group, because `run`, `display_name` and
// `group_source: start` must come from one lookup: a daemon that took the group
// from its live registry and the run from the runs.json mirror could publish
// `group_source: "start"` beside a null `run`.
type Registry interface {
	// Run answers the run that owns this port.
	Run(p state.Port) (run state.Run, found bool)
}

// SessionRegistry is the optional half of Registry. A registry that also knows
// the agent session behind a run reports it here, and AttributeWith stamps it
// onto the port; one that does not publishes a null session.
type SessionRegistry interface {
	Session(p state.Port) (session state.Session, ok bool)
}

// PortRuns reads the run attribution already stamped on the port row. It is
// not a registry of its own: the fields it reads are written by stampRuns from
// whichever registry the path brought (the daemon's live one, DirectRuns for a
// direct scan). Downstream adapters use it after attribution, and the scanner
// falls back to it when it runs without a registry at all.
type PortRuns struct{}

// Run returns the run already stamped on the port. Until `oberth start`
// records a group, run.group is empty, so this reports ok with only the name;
// the resolver then falls through to the file and git-root rules.
func (PortRuns) Run(p state.Port) (state.Run, bool) {
	if p.Run != nil {
		return *p.Run, true
	}
	return state.Run{}, false
}

// NoRuns is the empty run registry.
type NoRuns struct{}

// Run never matches.
func (NoRuns) Run(state.Port) (state.Run, bool) { return state.Run{}, false }

// Resolve fills Group, GroupSource and ProjectRoot on a copy of pp, applying
// the precedence chain from the daemon spec, first match wins:
//
//  1. start  — a `oberth start` run that owns the process
//  2. file   — a known `oberth.yaml` that claims the port
//  3. compose — the Compose project, unless its working directory is inside a
//     git checkout, in which case the container merges into that checkout's
//     group so a Compose db and a native api are one group
//  4. gitroot — the checkout containing the process cwd, named `<project>` or
//     `<project>@<worktree>`, where the project name comes from the main
//     checkout only: an alias from `groups.rename`, else the `name:` of its
//     `oberth.yaml`, else its directory name. A `oberth.yaml` at the
//     checkout's root makes the source `file`; a linked worktree's own copy
//     supplies services but never the name
//  5. none — group stays null
//
// A run keeps its group, except that a run recorded under the project's own
// name follows its checkout's current group name (see runGroup), and no config
// claims a port across a linked worktree's boundary.
//
// runs and index may both be nil.
func Resolve(pp []state.Port, runs Registry, index *Index) []state.Port {
	out := slices.Clone(pp)
	if index == nil {
		index = NewIndex()
	}
	// Pathless evidence has no per-row directory to reuse. Keep its direct
	// indexed-claim path rather than building filesystem memo tables for
	// potentially one different manifest per listener.
	pass := resolutionPassFor(index, pp)
	pass.resolve(out, runs)
	return out
}

// resolutionPassFor picks the pass shape for a batch: a fresh one as soon as a
// row carries a directory to observe, a bare one otherwise.
func resolutionPassFor(index *Index, pp []state.Port) *resolutionPass {
	for _, port := range pp {
		if port.Cwd != "" || (port.Docker != nil && index.ComposeDir(port.Docker.ComposeProject) != "") {
			return newResolutionPass(index)
		}
	}
	return &resolutionPass{index: index}
}

// resolve owns only the output array passed by its caller. Resolve copies a
// public input; AttributeWith already owns the newly converted wire rows.
func (r *resolutionPass) resolve(out []state.Port, runs Registry) {
	for i := range out {
		p := &out[i]
		co, inRepo := r.projectCheckout(p)
		p.ProjectRoot, p.Group, p.GroupSource = nil, nil, nil
		if inRepo {
			root := co.Root
			p.ProjectRoot = &root
		}
		if runs != nil {
			if run, ok := runs.Run(*p); ok && run.Group != "" {
				group := run.Group
				if inRepo {
					group = r.runGroup(group, co)
				}
				assign(p, group, state.SourceStart)
				continue
			}
		}
		if cfg, _, ok := r.index.matchPort(*p, r.matchClaims); ok && (!inRepo || r.within(cfg, co)) {
			assign(p, r.groupOf(cfg), state.SourceFile)
			continue
		}
		if inRepo {
			// Naming can discover the main manifest; query it before choosing
			// the source. No derived name/claim is memoized across mutations.
			name := r.checkoutName(co)
			source := state.SourceAuto
			if cfg := r.nearest(co.Root); cfg != nil && r.within(cfg, co) {
				source = state.SourceFile
			}
			assign(p, name, source)
			continue
		}
		if p.Docker != nil {
			assign(p, p.Docker.ComposeProject, state.SourceAuto)
		}
	}
}

func assign(p *state.Port, name string, source state.GroupSource) {
	if name == "" {
		return
	}
	group, from := name, source
	p.Group, p.GroupSource = &group, &from
}
