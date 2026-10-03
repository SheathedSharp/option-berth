package groups

import "github.com/sheathedsharp/option-berth/internal/state"

// Registry attributes a port to a process option-berth started. It answers with the
// whole run, not just its group, because `run`, `display_name` and
// `group_source: start` all have to come out of one lookup: a daemon that
// resolved the group from its live registry but the run from the runs.json
// mirror could publish `group_source: "start"` next to a null `run`.
type Registry interface {
	// Run returns the run that owns this port.
	Run(p state.Port) (run state.Run, ok bool)
}

// SessionRegistry is the optional half of Registry: a registry that also knows
// which agent session started a run reports it here, and AttributeWith stamps
// it onto the port (spec 2 §3, contract §5). A registry that does not
// implement it simply publishes ports with a null session.
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
// records a group (step 1A.5, contract §11.3) run.group is empty, so this
// reports ok with only the name; the resolver then falls through to the file
// and git-root rules.
func (PortRuns) Run(p state.Port) (state.Run, bool) {
	if p.Run == nil {
		return state.Run{}, false
	}
	return *p.Run, true
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
//     supplies services but never the name (step 5A.6)
//  5. none — group stays null
//
// A run's group is kept, except that a run recorded under the project's own
// name is moved to its checkout's current group name (see runGroup), and a
// config never claims a port across a linked worktree's boundary.
//
// runs and index may both be nil.
func Resolve(pp []state.Port, runs Registry, index *Index) []state.Port {
	out := make([]state.Port, len(pp))
	copy(out, pp)
	if index == nil {
		index = NewIndex()
	}
	// Pathless evidence has no per-row directory to reuse. Keep its direct
	// indexed-claim path rather than building filesystem memo tables for
	// potentially one different manifest per listener.
	pass := &resolutionPass{index: index}
	for _, port := range pp {
		if port.Cwd != "" || (port.Docker != nil && index.ComposeDir(port.Docker.ComposeProject) != "") {
			pass = newResolutionPass(index)
			break
		}
	}
	pass.resolve(out, runs)
	return out
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
	n, s := name, source
	p.Group, p.GroupSource = &n, &s
}
